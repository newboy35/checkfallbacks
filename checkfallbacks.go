// Utility for checking if fallback servers are working properly.
// It outputs failing servers info in STDOUT.  This allows this program to be
// used for automated testing of the fallback servers as a cron job.

package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/getlantern/errors"
	"github.com/getlantern/flashlight/chained"
	"github.com/getlantern/flashlight/client"
	"github.com/getlantern/flashlight/common"
	"github.com/getlantern/flashlight/config"
	"github.com/getlantern/fronted"
	"github.com/getlantern/golog"
	"github.com/getlantern/keyman"
	"github.com/getlantern/yaml"
)

const (
	// This is a special device ID that prevents checkfallbacks from being
	// throttled. Regular device IDs are Base64 encoded. Since Base64 encoding
	// doesn't use tildes, no regular device ID will ever match this special
	// string.
	DeviceID = "~~~~~~"
)

var (
	help          = flag.Bool("help", false, "Get usage help")
	verbose       = flag.Bool("verbose", false, "Be verbose (useful for manual testing)")
	fallbacksFile = flag.String("fallbacks", "fallbacks.json", "File containing json array of fallback information")
	numConns      = flag.Int("connections", 1, "Number of simultaneous connections")
	verify        = flag.Bool("verify", false, "Set to true to verify upstream connectivity")
)

var (
	log = golog.LoggerFor("checkfallbacks")
)

func main() {
	flag.Parse()

	if *help {
		flag.Usage()
		os.Exit(1)
	}

	numcores := runtime.NumCPU()
	runtime.GOMAXPROCS(numcores)
	log.Debugf("Using all %d cores on machine", numcores)

	initFronted()
	fallbacks := loadFallbacks(*fallbacksFile)
	outputCh := testAllFallbacks(fallbacks)
	for out := range outputCh {
		if out.err != nil {
			fmt.Printf("[failed fallback check] %v\n", out.err)
		}
		if *verbose && len(out.info) > 0 {
			for _, msg := range out.info {
				fmt.Printf("[output] %v\n", msg)
			}
		}
	}
}

func initFronted() {
	resp, err := http.Get("https://globalconfig.flashlightproxy.com/global.yaml.gz")
	if err != nil {
		log.Fatalf("Unable to get global config: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("Unexpected response status fetching global config: %v", resp.Status)
	}

	gzipReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		log.Fatalf("Unable to open gzip reader for global config: %v", err)
	}

	bytes, err := ioutil.ReadAll(gzipReader)
	if err != nil {
		log.Fatalf("Unable to read global config yaml: %v", err)
	}

	cfg := &config.Global{}
	err = yaml.Unmarshal(bytes, cfg)
	if err != nil {
		log.Fatalf("Unable to unmarshal global config yaml: %v", err)
	}

	certs := make([]string, 0, len(cfg.TrustedCAs))
	for _, ca := range cfg.TrustedCAs {
		certs = append(certs, ca.Cert)
	}
	pool, err := keyman.PoolContainingCerts(certs...)
	if err != nil {
		log.Fatalf("Could not create pool of trusted fronting CAs: %v", err)
	}

	fronted.Configure(pool, cfg.Client.MasqueradeSets, "masquerade_cache")
}

// Load the fallback servers list file. Failure to do so will result in
// exiting the program.
func loadFallbacks(filename string) (fallbacks [][]chained.ChainedServerInfo) {
	if filename == "" {
		log.Error("Please specify a fallbacks file")
		flag.Usage()
		os.Exit(2)
	}

	fileBytes, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatalf("Unable to read fallbacks file at %s: %s", filename, err)
	}

	err = json.Unmarshal(fileBytes, &fallbacks)
	if err != nil {
		log.Fatalf("Unable to unmarshal json from %v: %v", filename, err)
	}

	// Replace newlines in cert with newline literals
	for _, fbs := range fallbacks {
		for _, fb := range fbs {
			fb.Cert = strings.Replace(fb.Cert, "\n", "\\n", -1)
		}
	}
	return
}

type fullOutput struct {
	err  error
	info []string
}

// Test all fallback servers
func testAllFallbacks(fallbacks [][]chained.ChainedServerInfo) (output chan *fullOutput) {
	output = make(chan *fullOutput)

	// Make
	numFallbacks := 0
	for _, vals := range fallbacks {
		numFallbacks += len(vals)
	}
	fbChan := make(chan chained.ChainedServerInfo, numFallbacks)
	for _, vals := range fallbacks {
		for _, val := range vals {
			fbChan <- val
		}
	}
	close(fbChan)

	// Spawn goroutines and wait for them to finish
	go func() {
		workersWg := sync.WaitGroup{}

		log.Debugf("Spawning %d workers\n", *numConns)

		workersWg.Add(*numConns)
		for i := 0; i < *numConns; i++ {
			// Worker: consume fallback servers from channel and signal
			// Done() when closed (i.e. range exits)
			go func(i int) {
				for fb := range fbChan {
					output <- testFallbackServer(&fb, i)
				}
				workersWg.Done()
			}(i + 1)
		}
		workersWg.Wait()

		close(output)
	}()

	return
}

// Perform the test of an individual server
func testFallbackServer(fb *chained.ChainedServerInfo, workerID int) (output *fullOutput) {
	output = &fullOutput{}

	proto := "http"
	if fb.Cert != "" {
		proto = "https"
	}
	switch fb.PluggableTransport {
	case "obfs4":
		proto = "obfs4"
	case "lampshade":
		proto = "lampshade"
	}
	if fb.KCPSettings != nil && len(fb.KCPSettings) > 0 {
		proto = "kcp"
	}
	name := fmt.Sprintf("%v (%v)", fb.Addr, proto)
	log.Debugf("Testing %v", name)
	fb.MaxPreconnect = 1
	dialer, err := client.ChainedDialer(name, fb, DeviceID, func() string {
		return "" // pro-token
	})
	if err != nil {
		output.err = fmt.Errorf("%v: error building dialer: %v", fb.Addr, err)
		return
	}
	c := &http.Client{
		Transport: &http.Transport{
			Dial: func(network, addr string) (net.Conn, error) {
				dialer.Preconnect()
				select {
				case d := <-dialer.Preconnected():
					conn, _, err := d.DialContext(context.Background(), network, addr)
					return conn, err
				case <-time.After(15 * time.Second):
					return nil, errors.New("Timeout dialing")
				}
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	if *verify {
		verifyUpstream(fb, c, workerID, output)
	} else {
		ping(fb, c, workerID, output)
	}

	return
}

func ping(fb *chained.ChainedServerInfo, c *http.Client, workerID int, output *fullOutput) {
	req, err := http.NewRequest("GET", "http://ping-chained-server", nil)
	if err != nil {
		output.err = fmt.Errorf("%v: NewRequest to ping failed: %v", fb.Addr, err)
		return
	}
	req.Header.Set(common.PingHeader, "1") // request 1 KB
	doTest(fb, c, workerID, output, req, func(resp *http.Response, body []byte) error {
		if resp.StatusCode != 200 {
			return fmt.Errorf("%v: bad status code: %v", fb.Addr, resp.StatusCode)
		}
		if len(body) != 1024 {
			return fmt.Errorf("%v: wrong body size: %d", fb.Addr, len(body))
		}
		return nil
	})
}

func verifyUpstream(fb *chained.ChainedServerInfo, c *http.Client, workerID int, output *fullOutput) {
	req, err := http.NewRequest("GET", "http://linkdevice.nl/num.txt", nil)
	if err != nil {
		output.err = fmt.Errorf("%v: NewRequest to linkdevice.nl failed: %v", fb.Addr, err)
		return
	}
	doTest(fb, c, workerID, output, req, func(resp *http.Response, body []byte) error {
		if resp.StatusCode != 200 {
			return fmt.Errorf("%v: bad status code: %v", fb.Addr, resp.StatusCode)
		}

		if resp.Header.Get("x-amz-request-id") == "" {
			return fmt.Errorf("Upstream missing x-amz-request-id header")
		}
		if "12345678901234567890\n" != string(body) {
			return fmt.Errorf("Wrong response body: %v", string(body))
		}
		return nil
	})
}

func doTest(fb *chained.ChainedServerInfo, c *http.Client, workerID int, output *fullOutput, req *http.Request, verify func(resp *http.Response, body []byte) error) {
	req.Header.Set(common.DeviceIdHeader, DeviceID)
	req.Header.Set(common.TokenHeader, fb.AuthToken)

	if *verbose {
		reqStr, _ := httputil.DumpRequestOut(req, true)
		output.info = []string{"\n" + string(reqStr)}
	}

	resp, err := c.Do(req)
	if err != nil {
		output.err = fmt.Errorf("%v: ping failed: %v", fb.Addr, err)
		return
	}
	if *verbose {
		respStr, _ := httputil.DumpResponse(resp, true)
		output.info = append(output.info, "\n"+string(respStr))
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Debugf("Unable to close response body: %v", closeErr)
		}
	}()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		output.err = fmt.Errorf("%v: error reading response body: %v", fb.Addr, err)
		return
	}

	err = verify(resp, body)
	if err != nil {
		output.err = err
		return
	}

	log.Debugf("Worker %d: Fallback %v OK.\n", workerID, fb.Addr)
}

// Utility for checking if fallback servers are working properly.
// It outputs failing servers info in STDOUT.  This allows this program to be
// used for automated testing of the fallback servers as a cron job.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/getlantern/flashlight/chained"
	"github.com/getlantern/flashlight/client"
	"github.com/getlantern/flashlight/common"
	"github.com/getlantern/golog"
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
	verifyGeo     = flag.Bool("verify", false, "Set to true to verify upstream connectivity to geo.getiantem.org")
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
	dialer, err := client.ChainedDialer(name, fb, DeviceID, func() string {
		return "" // pro-token
	})
	if err != nil {
		output.err = fmt.Errorf("%v: error building dialer: %v", fb.Addr, err)
		return
	}
	c := &http.Client{
		Transport: &http.Transport{
			Dial: dialer.Dial,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	if *verifyGeo {
		geo(fb, c, workerID, output)
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

func geo(fb *chained.ChainedServerInfo, c *http.Client, workerID int, output *fullOutput) {
	req, err := http.NewRequest("GET", "http://geo.getiantem.org/lookup/", nil)
	if err != nil {
		output.err = fmt.Errorf("%v: NewRequest to geo.getiantem.org failed: %v", fb.Addr, err)
		return
	}
	doTest(fb, c, workerID, output, req, func(resp *http.Response, body []byte) error {
		if resp.StatusCode != 200 {
			return fmt.Errorf("%v: bad status code: %v", fb.Addr, resp.StatusCode)
		}
		if resp.Header.Get("X-Reflected-Ip") == "" {
			return fmt.Errorf("Geolookup missing X-Reflected-Ip header")
		}
		parsed := make(map[string]interface{}, 0)
		err := json.Unmarshal(body, &parsed)
		if err != nil {
			return fmt.Errorf("Unable to parse geolookup body: %v", err)
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

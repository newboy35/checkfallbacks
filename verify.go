package main

import (
	"net/http"
	"net/http/httputil"

	"github.com/getlantern/flashlight/chained"
	"github.com/getlantern/flashlight/common"
)

var (
	url = "http://www.bbc.co.uk"
)

func verifyFallback(fb *chained.ChainedServerInfo, c *http.Client) {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		log.Errorf("error make request to %s: %v", url, err)
		return
	}
	req.Header.Set(common.DeviceIdHeader, DeviceID)
	req.Header.Set(common.TokenHeader, fb.AuthToken)
	resp, err := c.Do(req)
	if err != nil {
		log.Errorf("%v: requesting %s failed: %v", fb.Addr, url, err)
		if *verbose {
			reqStr, _ := httputil.DumpRequestOut(req, true)
			log.Debug(string(reqStr))
		}
		return
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Debugf("Unable to close response body: %v", err)
		}
	}()
	if resp.StatusCode != http.StatusMovedPermanently {
		log.Errorf("%v: bad status code %v for %s", fb.Addr, resp.StatusCode, url)
		if *verbose {
			respStr, _ := httputil.DumpResponse(resp, true)
			log.Debug(string(respStr))
		}
		return
	}
	log.Debugf("%v via %s: OK.", url, fb.Addr)
}

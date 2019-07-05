package main

import (
	"reflect"
	"testing"

	"github.com/getlantern/flashlight/chained"
)

func TestJSONloading(t *testing.T) {
	logger := newLogger()
	logger.Debug("Running test")
	fallbacks := loadFallbacks("test.json")

	expectedFb := []*chained.ChainedServerInfo{
		&chained.ChainedServerInfo{
			Addr:      "78.62.239.134:443",
			Cert:      "-----CERTIFICATE-----\n",
			AuthToken: "a1",
		},
		&chained.ChainedServerInfo{
			Addr:      "178.62.239.34:80",
			Cert:      "-----CERTIFICATE-----\n",
			AuthToken: "a2",
		},
	}

	if len(expectedFb) != len(fallbacks) {
		t.Error("Expected number of fallbacks mismatch")
	}

	for i, f := range fallbacks {
		if !reflect.DeepEqual(f, expectedFb[i]) {
			t.Errorf("Got: %+v \n Expected: %+v", f, expectedFb[i])
		}
	}
}

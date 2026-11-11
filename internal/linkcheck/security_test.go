package linkcheck

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/coreydaley/messagepit/config"
)

func TestLinkCheckBlocksInternalAddress(t *testing.T) {
	previous := config.AllowInternalHTTPRequests
	config.AllowInternalHTTPRequests = false
	defer func() { config.AllowInternalHTTPRequests = previous }()

	var requests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer endpoint.Close()
	results := getHTTPStatuses([]string{endpoint.URL + "/private?token=private-token"}, true)
	if len(results) != 1 || results[0].StatusCode != 451 || results[0].Status != "Blocked private/reserved address" || requests.Load() != 0 {
		t.Fatal("link check did not report the blocked internal address")
	}
}

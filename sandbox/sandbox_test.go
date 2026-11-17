package sandbox_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anant-barlota/girvi-plugins/contract"
	"github.com/anant-barlota/girvi-plugins/sandbox"
	"github.com/anant-barlota/girvi-plugins/sandbox/sandboxtest"
	"github.com/anant-barlota/girvi-plugins/sdk"
)

func TestMemoryStore(t *testing.T) {
	sandboxtest.TestStore(t, func(*testing.T) sandbox.Store { return sandbox.NewMemoryStore(nil) })
}

// A grant that is not local never connects to this computer or network,
// however the name resolves: the address is checked when connecting.
func TestNetworkRefusesThisComputer(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	n := sandbox.NewNetwork()
	req := sdk.Request{Method: "POST", URL: srv.URL + "/?token=secret"}
	if r := n.Send(context.Background(), req, false); r.Error == "" || strings.Contains(r.Error, "secret") {
		t.Errorf("not local: %+v", r)
	}
	if r := n.Send(context.Background(), req, true); r.Error != "" || r.Status != 204 {
		t.Errorf("local: %+v", r)
	}
	if hits.Load() != 1 {
		t.Errorf("the server saw %d requests, want only the allowed one", hits.Load())
	}
}

func TestWakeTime(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for asked, want := range map[int64]time.Time{
		0:                 now.Add(contract.MinWakeDelay),
		now.Unix() + 3600: time.Unix(now.Unix()+3600, 0),
		1 << 62:           now.Add(contract.MaxDelay),
		-1 << 62:          now.Add(contract.MinWakeDelay),
	} {
		if got := sandbox.WakeTime(now, asked); !got.Equal(want) {
			t.Errorf("%d: %v, want %v", asked, got, want)
		}
	}
}

func TestRetryDelay(t *testing.T) {
	for seconds, want := range map[int]time.Duration{
		-5: time.Second, 0: time.Second, 300: 300 * time.Second, 1 << 62: contract.MaxDelay,
	} {
		if got := sandbox.RetryDelay(sdk.Retry(seconds, "")); got != want {
			t.Errorf("%d: %v, want %v", seconds, got, want)
		}
	}
}

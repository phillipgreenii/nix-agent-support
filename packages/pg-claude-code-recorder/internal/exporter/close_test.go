package exporter

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// A scrape stuck in its refresh outlives Shutdown's one-second grace; Close then
// drops the connection, so the scraper sees the connection end instead of
// hanging with the daemon gone.
func TestCloseDropsAScrapeShutdownCouldNotDrain(t *testing.T) {
	o := obs.New(nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	s, err := Start("127.0.0.1:0", o.Reg, func() { close(entered); <-release })
	if err != nil {
		t.Fatal(err)
	}
	defer close(release)
	got := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + s.Addr() + "/metrics")
		if err == nil {
			_ = resp.Body.Close()
		}
		got <- err
	}()
	<-entered
	start := time.Now()
	s.Close(context.Background())
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("Close took %v", d)
	}
	select {
	case err := <-got:
		if err == nil {
			t.Fatal("scrape completed normally; want its connection dropped")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the stuck scrape's connection is still open after Close: no hard-Close fallback")
	}
}

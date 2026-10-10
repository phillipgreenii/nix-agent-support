package daemon_test

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// healthReaders hammers the daemon's HTTP surface from several goroutines at
// once, each on its own keep-alive connection, and records what it saw. A
// request that cannot connect (the daemon is not listening yet, or no longer)
// is retried, so the readers can be started before the daemon is.
type healthReaders struct {
	client *http.Client
	url    func(path string) string
	stop   chan struct{}
	done   sync.Once
	wg     sync.WaitGroup

	ready   atomic.Int64 // answers of 200 from /readyz
	unready atomic.Int64 // answers of 503 from /readyz

	mu  sync.Mutex
	bad []string // answers no health read may give
}

func newHealthReaders(url func(path string) string) *healthReaders {
	return &healthReaders{
		client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 32}},
		url:    url,
		stop:   make(chan struct{}),
	}
}

// read starts n readers that each cycle through paths until finish.
func (h *healthReaders) read(n int, paths ...string) {
	for range n {
		h.wg.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-h.stop:
					return
				default:
				}
				path := paths[i%len(paths)]
				res, err := h.client.Get(h.url(path))
				if err != nil {
					time.Sleep(200 * time.Microsecond)
					continue
				}
				_, _ = io.Copy(io.Discard, res.Body)
				_ = res.Body.Close()
				h.record(path, res.StatusCode)
			}
		})
	}
}

// record notes an answer: /readyz is 200 or 503 not_ready, and every other
// health read is 200, whatever the daemon is doing.
func (h *healthReaders) record(path string, status int) {
	switch {
	case path == "/readyz" && status == http.StatusOK:
		h.ready.Add(1)
	case path == "/readyz" && status == http.StatusServiceUnavailable:
		h.unready.Add(1)
	case path != "/readyz" && status == http.StatusOK:
	default:
		h.mu.Lock()
		h.bad = append(h.bad, fmt.Sprintf("GET %s answered %d", path, status))
		h.mu.Unlock()
	}
}

// finish stops the readers and waits for them; it is safe to call twice. A
// test MUST NOT end with readers running: the next test may be given the port.
func (h *healthReaders) finish() {
	h.done.Do(func() { close(h.stop) })
	h.wg.Wait()
	h.client.CloseIdleConnections()
}

func (h *healthReaders) badAnswers() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.bad...)
}

// The health documents are built on the HTTP server's own goroutines, which
// are serving before the daemon has finished building itself, while the
// daemon's goroutine wires its alerter (start), swaps its configuration
// (reload) and closes the store (stop). Only the race detector can tell that
// those reads are synchronised with those writes, so this test fails only under
// `go test -race`: it reads /readyz and /healthz from before the listener is
// bound, through several starts and stops (the instants in which a read can
// meet a half-wired daemon are few, so each start is only a chance of it), and
// then, on the last start, keeps reading /readyz, /healthz and /metrics through
// forty reloads that move a running cycle's deadline across the clock (the
// alerter's reading of that cycle is what the reads walk), and through the stop.
func TestHealthReadsAreSynchronisedWithStartReloadAndStop(t *testing.T) {
	e := newEnv2(t, options{}) // the port and the configuration exist; nothing listens yet
	readers := newHealthReaders(e.url)
	defer readers.finish()
	t.Cleanup(func() {
		if e.d != nil {
			e.d.Stop()
		}
	})

	// Start. Readers on connections of their own reach the daemon as soon as
	// Start binds the port, and keep asking while it opens the store and wires
	// the alerter, the callbacks and the projection collector.
	readers.read(12, "/readyz", "/healthz")
	for range 6 {
		e.start(nil)
		e.d.Stop()
	}
	e.start(nil)
	readers.read(2, "/metrics") // the projection collector reads the alerter too
	eventually(t, "a /readyz answer of 200", func() bool { return readers.ready.Load() > 0 })

	// A cycle in overtime, so that the reading has a running cycle to report
	// and the scheduler's memory of it to read.
	e.bootstrap()
	e.clock.Set(local(9, 0))
	e.startCycle("deep-work") // 50 minutes
	e.clock.Set(local(9, 50)) // its time is up
	e.d.Alerter().Poll()

	// Reload. Each one polls the scheduler, which rewrites that memory while
	// the readers read it.
	for i := range 40 {
		minutes := 50 + 10*(i%2)
		e.writeConfig(func(c map[string]any) {
			c["cycles"].(map[string]any)["deep-work"].(map[string]any)["minutes"] = minutes
		})
		if err := e.d.Reload(); err != nil {
			t.Fatalf("reload %d: %v\nlog:\n%s", i, err, e.log.String())
		}
		e.clock.Advance(time.Minute)
	}

	// Stop, with the readers still asking.
	e.d.Stop()
	readers.finish()

	select {
	case <-e.d.Done():
	default:
		t.Error("the daemon is not done after Stop")
	}
	if bad := readers.badAnswers(); len(bad) > 0 {
		t.Errorf("health reads answered what they never may: %v", bad)
	}
	if readers.ready.Load() == 0 {
		t.Error("no /readyz answered 200")
	}
}

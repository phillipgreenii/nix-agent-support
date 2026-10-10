package daemon_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

type sseEvent struct{ name, data string }

// stream is an open /stream connection.
type stream struct {
	cancel context.CancelFunc
	ch     chan sseEvent
	hb     chan struct{}
}

func (e *env) openStream(t *testing.T) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", e.url("/api/v1/stream"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// No overall timeout: a stream outlives the ordinary client timeout.
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		cancel()
		t.Fatalf("/stream: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	s := &stream{cancel: cancel, ch: make(chan sseEvent, 64), hb: make(chan struct{}, 64)}
	go func() {
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == ": heartbeat":
				select {
				case s.hb <- struct{}{}:
				default:
				}
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if ev.name != "" {
					s.ch <- ev
				}
				ev = sseEvent{}
			}
		}
		close(s.ch)
	}()
	return s
}

func (s *stream) next(t *testing.T) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-s.ch:
		if !ok {
			t.Fatal("the stream ended")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a stream event")
	}
	return sseEvent{}
}

func (s *stream) close() { s.cancel() }

// /stream outlives the server's write timeout, heartbeats arrive, the version
// advances on a mutation and on a configuration reload, and every event comes
// from a client that sees the new version at once.
func TestStreamOutlivesTheWriteTimeoutAndCarriesChanges(t *testing.T) {
	e := newEnv(t, options{writeTimeout: 300 * time.Millisecond})
	s := e.openStream(t)
	defer s.close()
	first := s.next(t)
	if first.name != "state" || !strings.Contains(first.data, `"version"`) {
		t.Fatalf("first event: %+v", first)
	}

	// Heartbeats keep arriving past the write timeout.
	deadline := time.After(900 * time.Millisecond)
	beats := 0
loop:
	for {
		select {
		case <-s.hb:
			beats++
		case <-deadline:
			break loop
		}
	}
	if beats < 3 {
		t.Errorf("only %d heartbeats in 900ms with a 300ms write timeout and a 50ms heartbeat interval", beats)
	}

	// A mutation advances the version.
	e.bootstrap()
	ev := s.next(t)
	if ev.name != "state" || ev.data == first.data {
		t.Errorf("after a mutation: %+v (was %s)", ev, first.data)
	}
	// A configuration reload advances it too.
	e.writeConfig(func(c map[string]any) { c["group_order"] = []string{"End of day", "Start of day"} })
	if err := e.d.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	ev2 := s.next(t)
	if ev2.name != "state" || ev2.data == ev.data {
		t.Errorf("after a reload: %+v (was %s)", ev2, ev.data)
	}
	fams := e.scrape()
	if v, _ := sampleValue(fams["pg_task_focus_sse_clients"], nil); v != 1 {
		t.Errorf("sse_clients = %v, want 1", v)
	}
	if v, _ := sampleValue(fams["pg_task_focus_sse_events_sent_total"], nil); v < 3 {
		t.Errorf("sse_events_sent_total = %v, want at least 3", v)
	}
	s.close()
	eventually(t, "the client count to drop", func() bool {
		v, _ := sampleValue(e.scrape()["pg_task_focus_sse_clients"], nil)
		return v == 0
	})
}

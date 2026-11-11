package socket

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// runHandler serves one connection of h on an in-memory pipe, sends line, and
// returns the client end plus a channel closed when the handler returns.
func runHandler(t *testing.T, h proc.Handler, line string) (net.Conn, <-chan struct{}) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = server.Close() }()
		h(context.Background(), server, proc.Peer{})
	}()
	go func() { _, _ = client.Write([]byte(line)) }()
	t.Cleanup(func() { _ = client.Close() })
	return client, done
}

type capLog struct {
	mu    sync.Mutex
	lines []string
}

func (c *capLog) Log(change string, kv ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, change)
}

func (c *capLog) has(change string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.lines {
		if l == change {
			return true
		}
	}
	return false
}

func TestServerPanickingHandlerAnswersUnavailableAndServesTheNext(t *testing.T) {
	lg := &capLog{}
	var n int
	var mu sync.Mutex
	d := &stubDispatcher{fn: func(context.Context, router.Call) scriptout.Response {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			panic("boom")
		}
		return scriptout.Response{ProtocolVersion: scriptout.ProtocolVersion, Result: json.RawMessage(`{"ok":true}`)}
	}}
	h := Handler(d, ServerConfig{Logger: connectord.Logger(lg)})
	line := frameLine(ProtocolVersion, "i", 0, `{"op":"x"}`)

	for i, want := range []string{"unavailable", ""} {
		client, done := runHandler(t, h, line)
		_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
		raw, err := ReadFrame(bufio.NewReader(client), 1<<20)
		if err != nil {
			t.Fatalf("call %d: read reply: %v", i, err)
		}
		var rf ReplyFrame
		_ = json.Unmarshal(raw, &rf)
		var resp scriptout.Response
		_ = json.Unmarshal(rf.Response, &resp)
		got := ""
		if resp.Error != nil {
			got = resp.Error.Code
		}
		if got != want {
			t.Fatalf("call %d: error code %q, want %q", i, got, want)
		}
		_ = client.Close()
		<-done
	}
	if !lg.has("handler_panic") {
		t.Fatal("the panic was not logged")
	}
}

func TestServerReplyWriteHasADeadline(t *testing.T) {
	d := &stubDispatcher{}
	h := Handler(d, ServerConfig{WriteTimeout: 50 * time.Millisecond})
	// The client sends its frame and never reads the reply.
	_, done := runHandler(t, h, frameLine(ProtocolVersion, "i", 0, `{"op":"x"}`))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler is stuck writing to a client that stopped reading")
	}
}

func TestServerCancelsTheHandlerWhenThePeerCloses(t *testing.T) {
	started := make(chan struct{})
	cancelledAfter := make(chan time.Duration, 1)
	d := &stubDispatcher{fn: func(ctx context.Context, _ router.Call) scriptout.Response {
		close(started)
		t0 := time.Now()
		select {
		case <-ctx.Done():
			cancelledAfter <- time.Since(t0)
		case <-time.After(30 * time.Second):
			cancelledAfter <- 30 * time.Second
		}
		return scriptout.Response{ProtocolVersion: scriptout.ProtocolVersion}
	}}
	h := Handler(d, ServerConfig{WriteTimeout: 50 * time.Millisecond})
	client, done := runHandler(t, h, frameLine(ProtocolVersion, "i", 59000, `{"op":"x"}`))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}
	_ = client.Close()
	select {
	case dur := <-cancelledAfter:
		if dur > 5*time.Second {
			t.Fatalf("handler context cancelled after %s, want well under the frame deadline", dur)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handler context was not cancelled on peer close")
	}
	<-done
	_ = strings.TrimSpace
}

// Package client is the HTTP client of the pg-task-focus daemon: the thin
// layer every command-line verb uses. It speaks only the documented API and
// the event stream, sends X-Client, and turns the daemon's answers into Go
// values: the decoded success, or an error that says what the operator needs
// to know (the daemon is not reachable; the request was refused with a
// problem; the store is read-only).
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// DefaultAddr is where the daemon listens unless configured otherwise.
const DefaultAddr = "127.0.0.1:49210"

// Client talks to one daemon.
type Client struct {
	// Addr is host:port of the daemon's loopback listener.
	Addr string
	// Name is the X-Client value; it MUST be one of cli, web, connector or
	// swiftbar, and defaults to cli.
	Name string
	// HTTP is the transport; nil means a client with Timeout.
	HTTP *http.Client
	// Timeout bounds a request; zero means 15 seconds.
	Timeout time.Duration
}

// UnreachableError means the daemon could not be reached at all.
type UnreachableError struct {
	Addr string
	Err  error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("pg-task-focus is not reachable at %s (is the daemon running?): %v", e.Addr, e.Err)
}

func (e *UnreachableError) Unwrap() error { return e.Err }

// ProblemError is a refusal the daemon sent as application/problem+json.
type ProblemError struct {
	Problem wire.Problem
}

func (e *ProblemError) Error() string { return e.Problem.Detail }

// Reason is the problem's reason code.
func (e *ProblemError) Reason() string { return e.Problem.Reason }

// ReadOnly reports whether the refusal is store_unavailable from a store that
// is read-only.
func (e *ProblemError) ReadOnly() bool {
	return e.Problem.Reason == "store_unavailable" && e.Problem.Store != nil && e.Problem.Store.State == wire.StoreReadOnly
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	t := c.Timeout
	if t <= 0 {
		t = 15 * time.Second
	}
	return &http.Client{Timeout: t}
}

func (c *Client) name() string {
	if c.Name == "" {
		return "cli"
	}
	return c.Name
}

// Do sends one request and returns the body of a 2xx answer. A problem is a
// *ProblemError; a failure to connect is an *UnreachableError. body, when not
// nil, is sent as JSON.
func (c *Client) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.Addr+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Client", c.name())
	req.Header.Set("Accept", "application/json, application/problem+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, c.unreachable(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, c.unreachable(err)
	}
	if res.StatusCode/100 == 2 {
		return b, nil
	}
	var p wire.Problem
	if err := json.Unmarshal(b, &p); err != nil || p.Reason == "" {
		return nil, fmt.Errorf("the daemon answered %d with a body that is not a problem: %s", res.StatusCode, firstLine(b))
	}
	return nil, &ProblemError{Problem: p}
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (c *Client) unreachable(err error) error {
	return &UnreachableError{Addr: c.Addr, Err: err}
}

// State reads GET /state, returning the raw JSON and the decoded state.
func (c *Client) State(ctx context.Context) ([]byte, wire.State, error) {
	raw, err := c.Do(ctx, http.MethodGet, "/api/v1/state", nil)
	if err != nil {
		return nil, wire.State{}, err
	}
	var s wire.State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, wire.State{}, fmt.Errorf("the state is not the documented shape: %w", err)
	}
	return raw, s, nil
}

// Event is one server-sent event.
type Event struct {
	Name string
	Data string
}

// Stream opens GET /stream and calls fn for each event until the stream ends
// or fn returns an error. Comment lines (the heartbeat) are skipped. A stream
// that ends is returned as an *UnreachableError.
func (c *Client) Stream(ctx context.Context, fn func(Event) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.Addr+"/api/v1/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Client", c.name())
	req.Header.Set("Accept", "text/event-stream")
	// A stream has no overall timeout; the daemon's heartbeat keeps it alive.
	hc := &http.Client{}
	res, err := hc.Do(req)
	if err != nil {
		return c.unreachable(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		var p wire.Problem
		if json.Unmarshal(b, &p) == nil && p.Reason != "" {
			return &ProblemError{Problem: p}
		}
		return fmt.Errorf("the stream answered %d", res.StatusCode)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var ev Event
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if ev.Name != "" || ev.Data != "" {
				if err := fn(ev); err != nil {
					return err
				}
			}
			ev = Event{}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			ev.Name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			ev.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := sc.Err(); err != nil {
		return c.unreachable(err)
	}
	return &UnreachableError{Addr: c.Addr, Err: errors.New("the event stream ended")}
}

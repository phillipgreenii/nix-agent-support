// Package pprofget is a deliberately narrow HTTP GET client for a Go daemon's
// net/http/pprof endpoint on a unix socket.
//
// It is NOT a generic unix-socket or HTTP client. Three fixed constraints make
// that structural rather than a matter of caller discipline:
//
//   - the request method is GET and only GET (Client.Do refuses anything else
//     before it touches the network);
//   - the request path MUST be one of AllowedPaths, compared by exact string
//     equality (no cleaning, no query in the path, no prefix matching); and
//   - the socket MUST be an absolute path whose base name is SocketBase and
//     which is a unix socket, so a socket such as a container engine's API
//     socket is refused before any dial.
//
// The query string is never taken from a caller: it is built from the two
// validated Query fields, and only for the paths that accept them.
package pprofget

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SocketBase is the only socket file name Client accepts. It is the name the
// pprofserver standard gives every daemon's profiling socket.
const SocketBase = "pprof.sock"

// MaxSeconds is the largest ?seconds= value Client accepts for /profile and
// /trace.
const MaxSeconds = 300

// Prefix is the path prefix of every allowed path.
const Prefix = "/debug/pprof/"

// Index is the path of the profile index page.
const Index = Prefix

// profileNames are the runtime profiles served as /debug/pprof/<name>; they
// accept ?debug=N. They are the profiles net/http/pprof.Handler serves.
var profileNames = []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"}

// captureNames take ?seconds=N and hold the connection open while sampling.
var captureNames = []string{"profile", "trace"}

// otherNames take no query at all.
var otherNames = []string{"cmdline", "symbol"}

// AllowedPaths returns every path Client will request, sorted.
func AllowedPaths() []string {
	out := []string{Index}
	for _, group := range [][]string{profileNames, captureNames, otherNames} {
		for _, n := range group {
			out = append(out, Prefix+n)
		}
	}
	sort.Strings(out)
	return out
}

// RefusedError reports a request refused by policy before any network access.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return "refused: " + e.Reason }

// StatusError reports a non-2xx response from the daemon.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("daemon answered HTTP %d: %s", e.Status, strings.TrimSpace(e.Body))
}

// Query holds the only two query parameters Client can send. A nil field is
// omitted.
type Query struct {
	// Seconds is ?seconds=N, valid for profile and trace only (1..MaxSeconds).
	Seconds *int
	// Debug is ?debug=N, valid for the named runtime profiles only (0, 1 or 2).
	Debug *int
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ResolveTarget turns a user-facing target into an allowed path. It accepts a
// bare name ("heap"), "" or "index" for the index page, or a full allowed path
// ("/debug/pprof/heap"). Anything else is refused.
func ResolveTarget(target string) (string, error) {
	switch {
	case target == "" || target == "index":
		return Index, nil
	case strings.HasPrefix(target, "/"):
		if !contains(AllowedPaths(), target) {
			return "", &RefusedError{Reason: fmt.Sprintf("path %q is not on the pprof allowlist", target)}
		}
		return target, nil
	default:
		p := Prefix + target
		if strings.ContainsAny(target, "/?#%\\ \t\r\n") || !contains(AllowedPaths(), p) {
			return "", &RefusedError{Reason: fmt.Sprintf("target %q is not on the pprof allowlist", target)}
		}
		return p, nil
	}
}

func name(path string) string { return strings.TrimPrefix(path, Prefix) }

// buildQuery validates q against path and renders the query string.
func buildQuery(path string, q Query) (string, error) {
	n := name(path)
	v := url.Values{}
	if q.Seconds != nil {
		if !contains(captureNames, n) {
			return "", &RefusedError{Reason: "seconds is only valid for profile and trace"}
		}
		if *q.Seconds < 1 || *q.Seconds > MaxSeconds {
			return "", &RefusedError{Reason: fmt.Sprintf("seconds must be 1..%d", MaxSeconds)}
		}
		v.Set("seconds", strconv.Itoa(*q.Seconds))
	}
	if q.Debug != nil {
		if !contains(profileNames, n) {
			return "", &RefusedError{Reason: "debug is only valid for " + strings.Join(profileNames, ", ")}
		}
		if *q.Debug < 0 || *q.Debug > 2 {
			return "", &RefusedError{Reason: "debug must be 0, 1 or 2"}
		}
		v.Set("debug", strconv.Itoa(*q.Debug))
	}
	return v.Encode(), nil
}

// Client reads from one pprof socket.
type Client struct {
	// Socket is the absolute path of the daemon's pprof socket.
	Socket string
}

func (c *Client) checkSocket() error {
	if !filepath.IsAbs(c.Socket) {
		return &RefusedError{Reason: fmt.Sprintf("socket %q is not an absolute path", c.Socket)}
	}
	if filepath.Base(c.Socket) != SocketBase {
		return &RefusedError{Reason: fmt.Sprintf("socket %q is not named %s", c.Socket, SocketBase)}
	}
	fi, err := os.Lstat(c.Socket)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("no pprof socket at %s (is the daemon running with profiling on?)", c.Socket)
		}
		return err
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return &RefusedError{Reason: fmt.Sprintf("%s is not a unix socket", c.Socket)}
	}
	return nil
}

// Get requests path with the query q and copies the response body to w. It
// returns a *RefusedError for a policy refusal, a *StatusError for a non-2xx
// answer and any other error for a transport failure.
func (c *Client) Get(ctx context.Context, path string, q Query, w io.Writer) error {
	return c.Do(ctx, http.MethodGet, path, q, w)
}

// Do is Get with the method spelled out. Its only job beyond Get is to refuse
// every method but GET, so the refusal is testable and cannot be bypassed by
// reaching for a lower-level helper.
func (c *Client) Do(ctx context.Context, method, path string, q Query, w io.Writer) error {
	if method != http.MethodGet {
		return &RefusedError{Reason: fmt.Sprintf("method %q is not allowed; only GET", method)}
	}
	if !contains(AllowedPaths(), path) {
		return &RefusedError{Reason: fmt.Sprintf("path %q is not on the pprof allowlist", path)}
	}
	rawQuery, err := buildQuery(path, q)
	if err != nil {
		return err
	}
	if err := c.checkSocket(); err != nil {
		return err
	}

	socket := c.Socket
	hc := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
		// A redirect would be a second request to a path nobody allowlisted.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	u := url.URL{Scheme: "http", Host: "pprof", Path: path, RawQuery: rawQuery}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Status: resp.StatusCode, Body: string(snippet)}
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

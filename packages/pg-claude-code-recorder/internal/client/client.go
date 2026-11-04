// Package client is the recorder command's side of the wire: HTTP/1.1 with JSON
// bodies over the Unix socket.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// Client talks to one socket.
type Client struct {
	Socket string
	// Name is the --client value sent with each request.
	Name string
	HTTP *http.Client
}

// New returns a Client for socket.
func New(socket, name string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}
	return &Client{Socket: socket, Name: name, HTTP: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
}

// IsOffline reports whether err means nothing answers on the socket.
func IsOffline(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOTSOCK) ||
		errors.Is(err, syscall.EACCES)
}

// Do sends method path with an optional JSON body and decodes a 2xx answer into
// out (when non-nil). A non-2xx answer returns the daemon's *wire.Error; a
// socket that does not answer returns a daemon_offline *wire.Error.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return wire.Errf(wire.ErrInternal, err.Error())
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://recorder"+path, rd)
	if err != nil {
		return wire.Errf(wire.ErrInternal, err.Error())
	}
	if c.Name != "" {
		req.Header.Set(wire.ClientHeader, c.Name)
	}
	// The client's own budget minus a second, so the daemon answers
	// query_timeout before the client gives up on the connection.
	if c.HTTP.Timeout > 0 {
		if ms := (c.HTTP.Timeout - time.Second).Milliseconds(); ms > 0 {
			req.Header.Set(wire.DeadlineHeader, strconv.FormatInt(ms, 10))
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if IsOffline(err) {
			return wire.Errf(wire.ErrDaemonOffline, fmt.Sprintf("no answer on %s", c.Socket))
		}
		return wire.Errf(wire.ErrUnavailable, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return wire.Errf(wire.ErrUnavailable, err.Error())
	}
	if resp.StatusCode/100 != 2 {
		var we wire.Error
		if json.Unmarshal(data, &we) != nil || we.Name == "" {
			return wire.Errf(wire.ErrInternal, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(data)))
		}
		return &we
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return wire.Errf(wire.ErrInternal, "undecodable answer: "+err.Error())
		}
	}
	return nil
}

// Post is Do with POST /v1/<op>.
func (c *Client) Post(ctx context.Context, op string, body, out any) error {
	if body == nil {
		body = struct{}{}
	}
	return c.Do(ctx, http.MethodPost, "/v1/"+op, body, out)
}

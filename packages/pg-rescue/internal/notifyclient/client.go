// Package notifyclient posts one banner to the pg-osx-notifier daemon over its
// Unix socket (wire protocol v1). It is a small copy of the wire framing
// rather than a dependency: agent-support takes no flake dependency on
// support-apps. When the daemon cannot be reached the caller logs and drops;
// there is no fallback to osascript, terminal-notifier or SwiftBar.
package notifyclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	// Source is the metric-label token the daemon's `sources` allowlist holds
	// for this caller.
	Source = "pg-rescue"
	// DefaultTimeout is the response deadline: the daemon's own bound is 5 s,
	// so a client must wait at least 7 s.
	DefaultTimeout = 7 * time.Second

	maxReply = 1 << 20
)

// Client posts to the daemon.
type Client struct {
	// Getenv reads PG_OSX_NOTIFIER_SOCKET, XDG_STATE_HOME and HOME; os.Getenv
	// when nil.
	Getenv func(string) string
	// Timeout bounds one post, connect to reply; zero means 7 seconds.
	Timeout time.Duration
}

func (c Client) socket() (string, error) {
	getenv := c.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if p := getenv("PG_OSX_NOTIFIER_SOCKET"); p != "" {
		return p, nil
	}
	base := getenv("XDG_STATE_HOME")
	if base == "" {
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("no socket path: neither PG_OSX_NOTIFIER_SOCKET, XDG_STATE_HOME nor HOME is set")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "pg-osx-notifier", "pg-osx-notifier.sock"), nil
}

type request struct {
	ProtocolVersion int      `json:"protocolVersion"`
	Service         string   `json:"service"`
	Op              string   `json:"op"`
	Args            postArgs `json:"args"`
}

// postArgs carries only what this handler sends: never id, group, level,
// subtitle or url.
type postArgs struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Sound  string `json:"sound"`
	Source string `json:"source"`
}

type response struct {
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result json.RawMessage `json:"result"`
}

// Post sends one banner. The caller has already cut each field to the
// daemon's caps. Cancelling ctx closes the in-flight connection. The post is
// never retried: it carries no id, so a retry could duplicate the banner.
func (c Client) Post(ctx context.Context, title, body, sound string) error {
	path, err := c.socket()
	if err != nil {
		return err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	raw, err := json.Marshal(request{
		ProtocolVersion: 1, Service: "notify", Op: "post",
		Args: postArgs{Title: title, Body: body, Sound: sound, Source: Source},
	})
	if err != nil {
		return err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("notifier unreachable: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// A cancelled context (the orphan watch) closes the connection so a
	// blocked read returns at once.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if _, err := conn.Write(raw); err != nil {
		return fmt.Errorf("notifier write: %w", ctxErr(ctx, err, timeout))
	}
	var resp response
	if err := json.NewDecoder(io.LimitReader(conn, maxReply)).Decode(&resp); err != nil {
		return fmt.Errorf("notifier reply: %w", ctxErr(ctx, err, timeout))
	}
	if resp.Error != nil {
		return fmt.Errorf("notifier refused the post: %s: %s", resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Result) == 0 {
		return errors.New("notifier reply has neither result nor error")
	}
	return nil
}

func ctxErr(ctx context.Context, err error, timeout time.Duration) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded):
		return fmt.Errorf("no answer within %v", timeout)
	case ctx.Err() != nil:
		return fmt.Errorf("cancelled: %w", ctx.Err())
	}
	return err
}

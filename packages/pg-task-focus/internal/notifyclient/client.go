// Package notifyclient posts one banner to the pg-osx-notifier daemon over its
// Unix socket (wire protocol v1, ADR 0054 in support-apps). It is a small copy
// of the wire framing rather than a dependency: agent-support takes no flake
// dependency on support-apps. When the daemon cannot be reached the caller logs
// and drops; there is no fallback to osascript, terminal-notifier or SwiftBar.
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
	"unicode/utf8"
)

const (
	// Source is the metric-label token the daemon's `sources` allowlist holds
	// for this caller.
	Source = "pg-task-focus"
	// DefaultTimeout is the response deadline: the daemon's own bound is 5 s,
	// so a client must wait at least 7 s.
	DefaultTimeout = 7 * time.Second

	maxTitleBytes = 256
	maxBodyBytes  = 2048
	maxReply      = 1 << 20
)

// Client posts to the daemon. The zero value follows the discovery rule and
// the default deadline.
type Client struct {
	// SocketPath overrides discovery: $PG_OSX_NOTIFIER_SOCKET, else
	// ${XDG_STATE_HOME:-$HOME/.local/state}/pg-osx-notifier/pg-osx-notifier.sock.
	SocketPath string
	// Timeout bounds one post, connect to reply; zero means 7 seconds.
	Timeout time.Duration
}

func (c Client) socket() (string, error) {
	if c.SocketPath != "" {
		return c.SocketPath, nil
	}
	if p := os.Getenv("PG_OSX_NOTIFIER_SOCKET"); p != "" {
		return p, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("no socket path: %w", err)
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

type postArgs struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Sound  string `json:"sound,omitempty"`
	Source string `json:"source"`
}

type response struct {
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result json.RawMessage `json:"result"`
}

// Post sends one banner and returns a plain error for a transport failure or a
// daemon error reply. An empty sound omits the field (the daemon's default
// applies). The post is never retried: it carries no id, so a retry after a
// timeout could duplicate the banner.
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
		Args: postArgs{Title: CutBytes(title, maxTitleBytes), Body: CutBytes(body, maxBodyBytes), Sound: sound, Source: Source},
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
	if _, err := conn.Write(raw); err != nil {
		return fmt.Errorf("notifier write: %w", err)
	}
	var resp response
	if err := json.NewDecoder(io.LimitReader(conn, maxReply)).Decode(&resp); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return fmt.Errorf("notifier did not answer within %v", timeout)
		}
		return fmt.Errorf("notifier reply: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("notifier refused the post: %s: %s", resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Result) == 0 {
		return errors.New("notifier reply has neither result nor error")
	}
	return nil
}

// CutBytes cuts s to at most n bytes on a rune boundary.
func CutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

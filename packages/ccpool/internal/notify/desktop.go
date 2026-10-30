package notify

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
	// desktopSource is the metric-label token the daemon's `sources`
	// allowlist holds for ccpool.
	desktopSource = "ccpool"
	// defaultDesktopTimeout: the daemon's own bound is 5 s, so a client must
	// wait at least 7 s for its reply.
	defaultDesktopTimeout = 7 * time.Second

	maxTitleBytes = 256
	maxBodyBytes  = 2048
	maxReplyBytes = 1 << 20
)

// Desktop posts a banner through the pg-osx-notifier daemon over its Unix
// socket (wire protocol v1). It is a small copy of the wire framing rather
// than a dependency: agent-support takes no flake dependency on support-apps.
// When the daemon cannot be reached Notify returns an error that the caller
// logs and drops; there is no fallback to osascript, terminal-notifier or
// SwiftBar (the earlier beeep adapter did that). It is the local-workstation
// adapter only: on a headless/remote host use `exec`.
type Desktop struct {
	// Timeout bounds one post, connect to reply; zero means 7 seconds.
	Timeout time.Duration
}

// desktopSocketPath follows the discovery rule: $PG_OSX_NOTIFIER_SOCKET, else
// ${XDG_STATE_HOME:-$HOME/.local/state}/pg-osx-notifier/pg-osx-notifier.sock.
func desktopSocketPath() (string, error) {
	if p := os.Getenv("PG_OSX_NOTIFIER_SOCKET"); p != "" {
		return p, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("notify desktop: no socket path: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "pg-osx-notifier", "pg-osx-notifier.sock"), nil
}

type desktopRequest struct {
	ProtocolVersion int         `json:"protocolVersion"`
	Service         string      `json:"service"`
	Op              string      `json:"op"`
	Args            desktopArgs `json:"args"`
}

type desktopArgs struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Source string `json:"source"`
}

type desktopResponse struct {
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result json.RawMessage `json:"result"`
}

// Notify never retries: the post carries no id, so a retry after a timeout
// could duplicate the banner.
func (d Desktop) Notify(e Event) error {
	path, err := desktopSocketPath()
	if err != nil {
		return err
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = defaultDesktopTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	title, body := desktopText(e)
	raw, err := json.Marshal(desktopRequest{
		ProtocolVersion: 1, Service: "notify", Op: "post",
		Args: desktopArgs{Title: title, Body: body, Source: desktopSource},
	})
	if err != nil {
		return err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("notify desktop: notifier unreachable: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err := conn.Write(raw); err != nil {
		return fmt.Errorf("notify desktop: write: %w", err)
	}
	var resp desktopResponse
	if err := json.NewDecoder(io.LimitReader(conn, maxReplyBytes)).Decode(&resp); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return fmt.Errorf("notify desktop: notifier did not answer within %v", timeout)
		}
		return fmt.Errorf("notify desktop: reply: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("notify desktop: refused: %s: %s", resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Result) == 0 {
		return errors.New("notify desktop: reply has neither result nor error")
	}
	return nil
}

// desktopText builds the title `ccpool: <name>` (at most 256 B) and the body
// `<state> — <cwd>` (the state alone when the cwd is empty; at most 2048 B),
// cutting name and cwd on a rune boundary. The cwd is body text, never a path
// field.
func desktopText(e Event) (title, body string) {
	const prefix = "ccpool: "
	title = prefix + cutBytes(e.Name, maxTitleBytes-len(prefix))
	body = cutBytes(e.State, maxBodyBytes)
	if e.CWD != "" {
		const sep = " — "
		if room := maxBodyBytes - len(body) - len(sep); room > 0 {
			body += sep + cutBytes(e.CWD, room)
		}
	}
	return title, body
}

// cutBytes cuts s to at most n bytes on a rune boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

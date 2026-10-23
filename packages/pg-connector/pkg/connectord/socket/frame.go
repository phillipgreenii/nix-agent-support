// Package socket is the wire between a pg-connector daemon's client mode and
// the daemon: two frame types, the daemon-side server that plugs into the
// process shell's handler callback (proc.Handler), and the client-side dialer.
//
// # Frames
//
// One newline-delimited JSON frame each way per connection. The scriptout
// Request carries no deadline, instance or client version, so the client wraps
// it:
//
//	client to daemon: {socket_protocol, client_build, instance, deadline_ms, request}
//	daemon to client: {socket_protocol, daemon_build, response}
//
// request is the unmodified scriptout Request and response the unmodified
// scriptout response (its error body included) the client writes to stdout.
// socket_protocol is this wire's own version (ProtocolVersion); it is NOT
// scriptout.ProtocolVersion, the umbrella-to-backend envelope version, which
// the frames carry unchanged inside request and response.
//
// # Version window
//
// A daemon accepts a request whose socket_protocol is its own N or N-1 and
// answers anything else version_mismatch (Accepts). A build-id difference
// inside the window is not an error. A client accepts a reply whose
// socket_protocol is within one of its own (ReplyCompatible): a new daemon may
// answer an old client, and an old daemon refuses a client two versions ahead
// with its own version_mismatch reply, which the client relays.
//
// # Size cap
//
// A request frame over MaxFrameBytes (1 MiB, the frame's JSON excluding the
// trailing newline) is rejected with invalid_argument; the daemon never
// buffers more than the cap.
package socket

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ProtocolVersion is this binary's socket protocol number, N. It is a variable
// so a test can raise it to stand in for a newer or older peer; production code
// never assigns it. Version numbers start at 1: a frame without the field
// decodes as 0 and is never within a window.
var ProtocolVersion = 1

// MaxFrameBytes caps a request frame (the JSON of one line, without its
// newline): 1 MiB.
const MaxFrameBytes = 1 << 20

// ErrFrameTooLarge is returned by ReadFrame for a line over the cap.
var ErrFrameTooLarge = errors.New("frame exceeds the size cap")

// RequestFrame is the client-to-daemon frame.
type RequestFrame struct {
	SocketProtocol int    `json:"socket_protocol"`
	ClientBuild    string `json:"client_build"`
	// Instance is the registered instance name.
	Instance string `json:"instance"`
	// DeadlineMS is the call's remaining budget in milliseconds when the client
	// sent the frame; zero means the client states none and the daemon assumes
	// none.
	DeadlineMS int64 `json:"deadline_ms"`
	// Request is the unmodified scriptout Request.
	Request json.RawMessage `json:"request"`
}

// ReplyFrame is the daemon-to-client frame.
type ReplyFrame struct {
	SocketProtocol int    `json:"socket_protocol"`
	DaemonBuild    string `json:"daemon_build"`
	// Response is the unmodified scriptout response.
	Response json.RawMessage `json:"response"`
}

// Accepts reports whether a daemon at protocol current serves a request frame
// stamped with got: current or current-1, and never below 1.
func Accepts(current, got int) bool {
	return got >= 1 && (got == current || got == current-1)
}

// ReplyCompatible reports whether a client at protocol mine may relay a reply
// stamped with got: within one version of its own, in either direction.
func ReplyCompatible(mine, got int) bool {
	return got >= 1 && (got == mine || got == mine-1 || got == mine+1)
}

// ReadFrame reads one newline-terminated line of at most max bytes (excluding
// the newline) from br. A final line without a newline is accepted. A line over
// max returns ErrFrameTooLarge after reading at most max+1 bytes of it; end of
// input before any byte is io.EOF.
func ReadFrame(br *bufio.Reader, max int) ([]byte, error) {
	var buf bytes.Buffer
	for {
		chunk, err := br.ReadSlice('\n')
		buf.Write(chunk)
		switch {
		case err == nil:
			line := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
			if len(line) > max {
				return nil, ErrFrameTooLarge
			}
			return append([]byte(nil), line...), nil
		case errors.Is(err, bufio.ErrBufferFull):
			if buf.Len() > max {
				return nil, ErrFrameTooLarge
			}
		case errors.Is(err, io.EOF):
			if buf.Len() == 0 {
				return nil, io.EOF
			}
			if buf.Len() > max {
				return nil, ErrFrameTooLarge
			}
			return append([]byte(nil), buf.Bytes()...), nil
		default:
			return nil, err
		}
	}
}

// WriteFrame marshals v as one line (HTML escaping off, matching scriptout) and
// writes it with its newline in a single Write.
func WriteFrame(w io.Writer, v any) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode frame: %w", err)
	}
	_, err := w.Write(b.Bytes())
	return err
}

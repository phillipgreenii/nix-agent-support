// Package fakedaemon is a fake pg-osx-notifier socket server for tests. It
// records every request and answers per its mode; it never posts a banner.
package fakedaemon

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// OKReply is a successful notify/post reply.
const OKReply = `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"abc"}}`

// Server is a running fake daemon.
type Server struct {
	Path string

	mu       sync.Mutex
	requests []json.RawMessage
	// closed receives once per connection the client closed while the server
	// held its reply.
	closed chan struct{}
	hold   chan struct{}
	reply  string
	hang   bool
}

// Options picks the behaviour: Reply is the line written back; Hang holds the
// reply until the test ends (observing the client closing the connection).
type Options struct {
	Reply string
	Hang  bool
}

// Start listens on a short socket path (darwin limits sun_path).
func Start(t testing.TB, o Options) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pgr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s := &Server{Path: filepath.Join(dir, "s.sock"), closed: make(chan struct{}, 8), hold: make(chan struct{}), reply: o.Reply, hang: o.Hang}
	l, err := net.Listen("unix", s.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(s.hold); _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *Server) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	var v json.RawMessage
	if err := json.NewDecoder(c).Decode(&v); err != nil {
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, v)
	s.mu.Unlock()
	if s.hang {
		// Block reading: a client that closes its end gives us EOF.
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, c); close(done) }()
		select {
		case <-done:
			s.closed <- struct{}{}
		case <-s.hold:
		}
		return
	}
	_, _ = c.Write([]byte(s.reply + "\n"))
}

// Requests returns the raw requests received so far.
func (s *Server) Requests() []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]json.RawMessage(nil), s.requests...)
}

// Closed is signalled once for each connection the client closed while the
// server was holding its reply.
func (s *Server) Closed() <-chan struct{} { return s.closed }

// Request is the decoded shape of a notify/post request.
type Request struct {
	ProtocolVersion int
	Service, Op     string
	Args            map[string]any
}

// Decode parses a raw request.
func Decode(t testing.TB, raw json.RawMessage) Request {
	t.Helper()
	var r Request
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

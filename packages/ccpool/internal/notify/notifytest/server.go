// Package notifytest is a fake pg-osx-notifier socket server for tests of the
// Desktop adapter and of the callers that route through it.
package notifytest

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Request is one decoded request as the daemon would see it.
type Request struct {
	ProtocolVersion int
	Service         string
	Op              string
	Args            map[string]any
}

// Server answers each connection with one canned reply.
type Server struct {
	// Path is the socket path (also exported as PG_OSX_NOTIFIER_SOCKET by Start).
	Path string

	mu   sync.Mutex
	reqs []Request
	// Reply is written after each request; empty means a success reply.
	// Hang stops the server from answering at all (until it is closed).
	Reply string
	Hang  bool
	ln    net.Listener
}

// Start listens on a short socket path (macOS limits sun_path to 104 bytes),
// points PG_OSX_NOTIFIER_SOCKET at it and closes it when the test ends.
func Start(t *testing.T) *Server {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s := &Server{Path: filepath.Join(dir, "n.sock")}
	s.ln, err = (&net.ListenConfig{}).Listen(t.Context(), "unix", s.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ln.Close() })
	t.Setenv("PG_OSX_NOTIFIER_SOCKET", s.Path)
	go s.serve()
	return s
}

func (s *Server) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var raw struct {
		ProtocolVersion int            `json:"protocolVersion"`
		Service         string         `json:"service"`
		Op              string         `json:"op"`
		Args            map[string]any `json:"args"`
	}
	if err := json.NewDecoder(c).Decode(&raw); err != nil {
		return
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, Request(raw))
	reply, hang := s.Reply, s.Hang
	s.mu.Unlock()
	if hang {
		time.Sleep(3 * time.Second)
		return
	}
	if reply == "" {
		reply = `{"protocolVersion":1,"schemaVersion":1,"result":{"id":"x"}}`
	}
	_, _ = c.Write([]byte(reply + "\n"))
}

// Requests returns a copy of the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// SetReply and SetHang change the behavior for later connections.
func (s *Server) SetReply(r string) { s.mu.Lock(); s.Reply = r; s.mu.Unlock() }
func (s *Server) SetHang(h bool)    { s.mu.Lock(); s.Hang = h; s.mu.Unlock() }

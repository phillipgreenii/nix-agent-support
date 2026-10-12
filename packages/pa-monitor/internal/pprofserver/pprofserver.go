// Package pprofserver exposes Go's runtime profiling endpoints (net/http/pprof)
// for a long-running daemon, on a listener that is never reachable from the
// network.
//
// A daemon enables it by setting one environment variable to either:
//
//   - an absolute path: a unix socket created with mode 0600 (preferred; needs
//     no port, and only the owning user can connect), or
//   - a loopback "host:port" (127.0.0.1, localhost or ::1) for tools that cannot
//     dial a unix socket.
//
// Anything else (":6060", "0.0.0.0:6060", a relative path, a remote host) is
// refused, so a typo can never publish the profiler on a LAN address.
//
// net/http/pprof registers its handlers on http.DefaultServeMux at import time.
// This package never serves the default mux; it serves its own, holding only
// the pprof routes, so an unrelated handler registered elsewhere in the process
// cannot leak onto this listener.
package pprofserver

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Server is a running profiling listener. Close it to stop serving.
type Server struct {
	srv      *http.Server
	listener net.Listener
	// socket is the unix socket path to remove on Close, "" for TCP.
	socket    string
	closeOnce sync.Once
}

// Start validates addr, opens the listener and serves pprof on it in the
// background. See the package comment for the accepted forms.
func Start(addr string) (*Server, error) {
	l, socket, err := listen(addr)
	if err != nil {
		return nil, err
	}
	s := &Server{
		// No WriteTimeout: /debug/pprof/profile?seconds=N and /trace hold the
		// response open for N seconds by design.
		srv:      &http.Server{Handler: newMux(), ReadHeaderTimeout: 5 * time.Second},
		listener: l,
		socket:   socket,
	}
	go func() { _ = s.srv.Serve(l) }()
	return s, nil
}

// Addr is the address the server is listening on: the socket path, or the
// resolved host:port (useful when the port was 0).
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Close stops the server and removes the unix socket file. It is idempotent.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		_ = s.srv.Close()
		if s.socket != "" {
			_ = os.Remove(s.socket)
		}
	})
}

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// listen opens the listener for addr and returns the unix socket path it
// created ("" for TCP).
func listen(addr string) (net.Listener, string, error) {
	if addr == "" {
		return nil, "", errors.New("pprofserver: empty address")
	}
	if filepath.IsAbs(addr) {
		l, err := listenUnix(addr)
		return l, addr, err
	}
	l, err := listenLoopback(addr)
	return l, "", err
}

func listenUnix(path string) (net.Listener, error) {
	// Replace a socket left by a crashed process, but never anything else: the
	// path comes from configuration and must not be able to delete a real file.
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode().Type() != fs.ModeSocket {
			return nil, fmt.Errorf("pprofserver: %s exists and is not a socket; refusing to replace it", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("pprofserver: remove stale socket: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("pprofserver: stat %s: %w", path, err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("pprofserver: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("pprofserver: chmod %s: %w", path, err)
	}
	return l, nil
}

func listenLoopback(addr string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("pprofserver: %q is neither an absolute socket path nor a loopback host:port: %w", addr, err)
	}
	switch host {
	case "127.0.0.1", "localhost":
		host = "127.0.0.1"
	case "::1":
	default:
		return nil, fmt.Errorf("pprofserver: host %q is not loopback (use 127.0.0.1, localhost or ::1, or an absolute socket path)", host)
	}
	l, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("pprofserver: listen %s: %w", addr, err)
	}
	return l, nil
}

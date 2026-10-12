// Package proc is the process shell of a pg-connector daemon: the lifecycle
// every daemon-backed connector shares, and the one platform seam under it.
//
// # Shell
//
// Start prepares the process: it creates the state directory with mode 0700,
// takes the single-instance lock in it, removes a stale socket left by a dead
// instance, refuses a socket path longer than the platform's sun_path limit,
// and listens on a unix socket with mode 0600. Serve then accepts connections,
// rejects a peer whose uid differs from the daemon's, and hands every other
// connection to a Handler; when its context ends (the binary cancels it on
// SIGTERM) it drains: it stops accepting, lets in-flight handlers finish,
// runs the registered drain hooks in order, and releases the lock, so that a
// second process starts cleanly. Run is Start followed by Serve.
//
// Start and Serve are separate so the assembling daemon can do slow startup
// work (open the store and apply migrations) while connections are already
// accepted-or-queued: take the lock with Start, start the work in a goroutine,
// then call Serve with a Handler that answers "unavailable, migrating" until
// the work finishes.
//
// A stale socket needs no connect probe: the lock is the proof of life. A
// process that holds the lock is the only one entitled to the socket path, so
// whatever socket file is there when Start holds the lock was left by a dead
// instance (SIGKILL leaves one) and is removed.
//
// # Seam
//
// Everything platform-specific sits behind four functions, one
// implementation per build tag (*_darwin.go, *_linux.go): PeerUID,
// MaxSocketPathBytes, SupervisorProbe and RestartHint. Config lets a test
// inject PeerUID and the socket-path limit.
package proc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
)

// DefaultDrainTimeout bounds how long a drain waits for in-flight handlers
// when Config.DrainTimeout is zero.
const DefaultDrainTimeout = 30 * time.Second

// DefaultLockFileName is the lock file inside the state directory when
// Config.LockPath is empty.
const DefaultLockFileName = "daemon.lock"

// Modes of the state directory, the lock file and the socket.
const (
	stateDirMode = 0o700
	socketMode   = 0o600
	lockFileMode = 0o600
)

// Config is what the shell needs, independent of any one connector's config
// type.
type Config struct {
	// StateDir is the absolute state directory (REQUIRED). It is created with
	// mode 0700 and an existing one is tightened to 0700.
	StateDir string
	// SocketPath is the absolute unix socket path (REQUIRED).
	SocketPath string
	// LockPath is the single-instance lock file; default StateDir/daemon.lock.
	LockPath string
	// Logger receives the shell's own lines (lock_held, peer_uid_rejected,
	// socket_path_too_long, ...); nil discards them.
	Logger connectord.Logger
	// DrainTimeout bounds the wait for in-flight handlers on drain; zero
	// means DefaultDrainTimeout. On expiry the remaining connections are
	// closed and their handlers' contexts are cancelled.
	DrainTimeout time.Duration

	// PeerUID overrides the seam's PeerUID (tests). nil uses PeerUID.
	PeerUID func(net.Conn) (uint32, error)
	// OwnUID overrides os.Getuid() as the daemon's uid (tests). nil uses it.
	OwnUID func() uint32
	// MaxSocketPathBytes overrides the seam's limit (tests). Zero uses
	// MaxSocketPathBytes().
	MaxSocketPathBytes int
}

// Peer is the verified identity of a connection's other end.
type Peer struct {
	UID uint32
}

// Handler serves one accepted connection from a verified peer. The shell
// closes conn when Handler returns. ctx is cancelled only if a drain times out
// (in-flight handlers are otherwise allowed to finish), so a handler SHOULD
// watch it on long operations.
type Handler func(ctx context.Context, conn net.Conn, peer Peer)

// DrainHook runs once during a drain, after in-flight handlers finish and
// before the lock is released. The assembling daemon registers its work here
// (the store's WAL checkpoint is the last hook).
type DrainHook struct {
	Name string
	Run  func(ctx context.Context) error
}

// Hooks is the drain-hook registry. Hooks run in registration order. The zero
// value is ready to use and safe for concurrent Register calls.
type Hooks struct {
	mu    sync.Mutex
	hooks []DrainHook
}

// Register appends a hook; it runs after every hook registered before it.
func (h *Hooks) Register(name string, run func(ctx context.Context) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hooks = append(h.hooks, DrainHook{Name: name, Run: run})
}

// snapshot returns the hooks registered so far, in order.
func (h *Hooks) snapshot() []DrainHook {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]DrainHook(nil), h.hooks...)
}

// ErrLocked is returned (wrapped) by Start when another instance holds the
// state directory's lock.
var ErrLocked = errors.New("another instance holds the lock")

// ErrSocketPathTooLong is returned (wrapped) by Start for a socket path over
// the platform limit.
var ErrSocketPathTooLong = errors.New("socket path exceeds the platform limit")

// CheckSocketPath reports whether path fits sockaddr_un.sun_path of limit
// bytes: the path plus its terminating NUL must fit, so at most limit-1 bytes.
// The error names the path length and the limit.
func CheckSocketPath(path string, limit int) error {
	if len(path) >= limit {
		return fmt.Errorf("%w: %q is %d bytes, but the platform's sun_path limit is %d bytes including the terminating NUL (at most %d bytes of path); use a shorter state_dir or socket_path",
			ErrSocketPathTooLong, path, len(path), limit, limit-1)
	}
	return nil
}

// Server is a started shell: it holds the lock and the listening socket.
type Server struct {
	cfg      Config
	log      connectord.Logger
	lock     *flock.Flock
	ln       net.Listener
	ownUID   uint32
	peerUID  func(net.Conn) (uint32, error)
	released bool
}

// Start prepares the process shell: path check, state directory, lock, stale
// socket removal, listen, socket mode. On any error nothing is left held.
func Start(cfg Config) (*Server, error) {
	log := cfg.Logger
	if log == nil {
		log = connectord.NopLogger
	}
	if cfg.StateDir == "" || !filepath.IsAbs(cfg.StateDir) {
		return nil, fmt.Errorf("state directory %q must be an absolute path", cfg.StateDir)
	}
	if cfg.SocketPath == "" || !filepath.IsAbs(cfg.SocketPath) {
		return nil, fmt.Errorf("socket path %q must be an absolute path", cfg.SocketPath)
	}
	limit := cfg.MaxSocketPathBytes
	if limit == 0 {
		limit = MaxSocketPathBytes()
	}
	if err := CheckSocketPath(cfg.SocketPath, limit); err != nil {
		log.Log("socket_path_too_long", "socket_path", cfg.SocketPath, "bytes", len(cfg.SocketPath), "limit", limit)
		return nil, err
	}

	if err := os.MkdirAll(cfg.StateDir, stateDirMode); err != nil {
		return nil, fmt.Errorf("create state directory %s: %w", cfg.StateDir, err)
	}
	// MkdirAll applies the umask and leaves an existing directory alone, so
	// the mode is set explicitly.
	if err := os.Chmod(cfg.StateDir, stateDirMode); err != nil {
		return nil, fmt.Errorf("set state directory %s to mode %04o: %w", cfg.StateDir, stateDirMode, err)
	}

	lockPath := cfg.LockPath
	if lockPath == "" {
		lockPath = filepath.Join(cfg.StateDir, DefaultLockFileName)
	}
	lk := flock.New(lockPath, flock.SetFlag(os.O_CREATE|os.O_RDWR), flock.SetPermissions(lockFileMode))
	locked, err := lk.TryLock()
	if err != nil {
		return nil, fmt.Errorf("take the instance lock %s: %w", lockPath, err)
	}
	if !locked {
		log.Log("lock_held", "lock", lockPath)
		return nil, fmt.Errorf("%w: lock %s is held (is another pg-connector daemon running on state directory %s?)", ErrLocked, lockPath, cfg.StateDir)
	}
	// From here every error path releases the lock.
	fail := func(err error) (*Server, error) {
		_ = lk.Unlock()
		return nil, err
	}

	if err := removeStaleSocket(cfg.SocketPath, log); err != nil {
		return fail(err)
	}
	ln, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return fail(fmt.Errorf("listen on %s: %w", cfg.SocketPath, err))
	}
	if err := os.Chmod(cfg.SocketPath, socketMode); err != nil {
		_ = ln.Close()
		return fail(fmt.Errorf("set socket %s to mode %04o: %w", cfg.SocketPath, socketMode, err))
	}

	s := &Server{cfg: cfg, log: log, lock: lk, ln: ln, ownUID: uint32(os.Getuid()), peerUID: PeerUID}
	if cfg.OwnUID != nil {
		s.ownUID = cfg.OwnUID()
	}
	if cfg.PeerUID != nil {
		s.peerUID = cfg.PeerUID
	}
	log.Log("shell_started", "state_dir", cfg.StateDir, "socket_path", cfg.SocketPath, "lock", lockPath)
	return s, nil
}

// removeStaleSocket removes a socket file left at path by a dead instance. The
// caller holds the lock, so no live instance owns it. A path that exists but is
// not a socket is refused, never deleted: it is somebody's file.
func removeStaleSocket(path string, log connectord.Logger) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("inspect socket path %s: %w", path, err)
	case fi.Mode()&fs.ModeSocket == 0:
		return fmt.Errorf("socket path %s exists and is not a socket; refusing to remove it", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	log.Log("stale_socket_removed", "socket_path", path)
	return nil
}

// Addr is the address the server listens on.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Close releases the lock and the socket without serving, for a caller whose
// startup failed after Start. Serve does this itself; calling Close after
// Serve returned is a no-op.
func (s *Server) Close() error {
	_ = s.ln.Close()
	s.releaseLocked()
	return nil
}

func (s *Server) releaseLocked() {
	if s.released {
		return
	}
	s.released = true
	_ = os.Remove(s.cfg.SocketPath)
	_ = s.lock.Unlock()
}

// Serve accepts connections until ctx ends, then drains (see the package doc)
// and returns nil, or the joined errors of failed drain hooks. A listener
// failure that is not a shutdown also drains and is returned.
func (s *Server) Serve(ctx context.Context, handler Handler, hooks *Hooks) error {
	var (
		wg       sync.WaitGroup
		connsMu  sync.Mutex
		conns    = map[net.Conn]struct{}{}
		handlers = context.WithoutCancel(ctx)
	)
	hctx, cancelHandlers := context.WithCancel(handlers)
	defer cancelHandlers()

	// A cancelled ctx closes the listener, which unblocks Accept.
	stop := context.AfterFunc(ctx, func() { _ = s.ln.Close() })
	defer stop()

	var acceptErr error
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				acceptErr = fmt.Errorf("accept on %s: %w", s.cfg.SocketPath, err)
				s.log.Log("accept_failed", "socket_path", s.cfg.SocketPath, "error", err.Error())
			}
			break
		}
		peer, ok := s.verify(conn)
		if !ok {
			_ = conn.Close()
			continue
		}
		connsMu.Lock()
		conns[conn] = struct{}{}
		connsMu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				connsMu.Lock()
				delete(conns, conn)
				connsMu.Unlock()
				_ = conn.Close()
			}()
			handler(hctx, conn, peer)
		}()
	}

	// Drain: no new connections now (the listener is closed).
	s.log.Log("drain_started")
	_ = s.ln.Close()
	timeout := s.cfg.DrainTimeout
	if timeout <= 0 {
		timeout = DefaultDrainTimeout
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	timer := time.NewTimer(timeout)
	select {
	case <-done:
		timer.Stop()
	case <-timer.C:
		s.log.Log("drain_timeout", "timeout", timeout.String())
		cancelHandlers()
		connsMu.Lock()
		for c := range conns {
			_ = c.Close()
		}
		connsMu.Unlock()
		<-done
	}

	// Hooks get a bounded context of their own: ctx is already cancelled.
	hookCtx, cancelHooks := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancelHooks()
	errs := []error{acceptErr}
	for _, h := range hooks.snapshot() {
		if err := h.Run(hookCtx); err != nil {
			s.log.Log("drain_hook_failed", "hook", h.Name, "error", err.Error())
			errs = append(errs, fmt.Errorf("drain hook %s: %w", h.Name, err))
			continue
		}
		s.log.Log("drain_hook_done", "hook", h.Name)
	}
	s.releaseLocked()
	s.log.Log("drain_done")
	return errors.Join(errs...)
}

// verify checks the peer's uid against the daemon's. A failed credential call
// rejects the connection too: an unverifiable peer is not a trusted one.
func (s *Server) verify(conn net.Conn) (Peer, bool) {
	uid, err := s.peerUID(conn)
	if err != nil {
		s.log.Log("peer_cred_failed", "error", err.Error())
		return Peer{}, false
	}
	if uid != s.ownUID {
		s.log.Log("peer_uid_rejected", "peer_uid", uid, "daemon_uid", s.ownUID)
		return Peer{}, false
	}
	return Peer{UID: uid}, true
}

// Run is Start followed by Serve. hooks MAY be nil.
func Run(ctx context.Context, cfg Config, handler Handler, hooks *Hooks) error {
	s, err := Start(cfg)
	if err != nil {
		return err
	}
	return s.Serve(ctx, handler, hooks)
}

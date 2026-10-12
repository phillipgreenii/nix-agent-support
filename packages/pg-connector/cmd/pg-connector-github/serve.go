package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
)

// exitServe is the exit status of a serve that refused to start or failed
// while draining (lock held, socket path too long, an unusable state
// directory, a failed drain hook).
const exitServe = 5

// runServe runs the daemon's process shell: lock, socket, accept, and drain on
// SIGTERM (or SIGINT). The handler answers nothing yet; the socket protocol is
// later work. It returns 0 after a clean drain.
func runServe(cfg *config.Config, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return serve(ctx, cfg, stderr)
}

// serve is runServe with the shutdown context injected.
func serve(ctx context.Context, cfg *config.Config, stderr io.Writer) int {
	hooks := &proc.Hooks{}
	err := proc.Run(ctx, shellConfig(cfg, stderr), answerNothing, hooks)
	if err != nil {
		fmt.Fprintf(stderr, "pg-connector-github serve: %v\n", err)
		return exitServe
	}
	return exitOK
}

// shellConfig maps the loaded config onto the process shell's.
func shellConfig(cfg *config.Config, stderr io.Writer) proc.Config {
	return proc.Config{
		StateDir:   cfg.StateDir,
		SocketPath: cfg.SocketPath,
		LockPath:   filepath.Join(cfg.StateDir, config.LockFileName),
		Logger:     newStderrLogger(stderr),
	}
}

// answerNothing is the handler until the socket protocol exists: the shell
// closes the connection when it returns.
func answerNothing(context.Context, net.Conn, proc.Peer) {}

// stderrLogger writes one line per state change to a writer, until the
// daemon's own structured log exists: "<RFC3339> <change> key=value ...".
type stderrLogger struct {
	mu sync.Mutex
	w  io.Writer
}

func newStderrLogger(w io.Writer) connectord.Logger { return &stderrLogger{w: w} }

func (l *stderrLogger) Log(change string, kv ...any) {
	var b strings.Builder
	b.WriteString(time.Now().UTC().Format(time.RFC3339))
	b.WriteString(" pg-connector-github ")
	b.WriteString(change)
	for i := 0; i < len(kv); i += 2 {
		if i+1 < len(kv) {
			fmt.Fprintf(&b, " %v=%q", kv[i], fmt.Sprint(kv[i+1]))
		} else {
			fmt.Fprintf(&b, " %v", kv[i])
		}
	}
	b.WriteByte('\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.w, b.String())
}

// Command beads-exporter publishes bead-tracker content metrics as Prometheus
// text. A background poller reads each configured beads database through a
// pinned, read-only bd and swaps an immutable snapshot in atomically, so a
// scrape never waits on collection.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/collect"
	"github.com/phillipgreenii/beads-exporter/internal/config"
	"github.com/phillipgreenii/beads-exporter/internal/logx"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
	"github.com/phillipgreenii/beads-exporter/internal/sched"
	"github.com/phillipgreenii/beads-exporter/internal/server"
)

// Version is stamped by the build (-X main.Version=...).
var Version = "dev"

// Exit codes. 1 is reserved for unexpected errors; 2 and above carry meaning.
const (
	exitOK       = 0
	exitInternal = 1
	exitUsage    = 2
	exitConfig   = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("beads-exporter", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path of the JSON configuration file to run with")
	checkPath := fs.String("check-config", "", "validate the configuration file's schema and exit; touches no database or bd binary")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	switch {
	case *showVersion:
		fmt.Fprintln(stdout, Version)
		return exitOK
	case *checkPath != "":
		if _, err := config.Load(*checkPath); err != nil {
			fmt.Fprintln(stderr, err)
			return exitConfig
		}
		fmt.Fprintln(stdout, "ok")
		return exitOK
	case *configPath == "":
		fmt.Fprintln(stderr, "usage: beads-exporter -config <file> | -check-config <file> | -version")
		return exitUsage
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitConfig
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Opt-in profiling endpoint (BEADS_EXPORTER_PPROF_ADDR; the serve path only,
	// never -version or -check-config). Reports on stderr, never stdout (the JSON
	// log stream), and a rejected value never stops the exporter.
	defer startPprofFromEnv(os.Getenv, stderr)()
	log := logx.New(stdout, slog.LevelInfo)
	if err := serve(ctx, cfg, log); err != nil {
		log.Error("exporter stopped", "error", err.Error())
		return exitInternal
	}
	return exitOK
}

// serve runs the pollers and the HTTP listener until ctx is cancelled.
func serve(parent context.Context, cfg *config.Config, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	home := os.Getenv("HOME")
	if home == "" {
		return errors.New("HOME is not set; it is re-asserted on every bd child")
	}

	dbs := make([]collect.DB, 0, len(cfg.DBNames))
	for _, name := range cfg.DBNames {
		dbs = append(dbs, collect.DB{
			Name: name,
			Adapter: bd.NewClient(bd.ClientConfig{
				BDPath:    cfg.BDPath,
				BeadsDir:  cfg.BeadsDirs[name],
				Home:      home,
				ChildPath: cfg.ChildPath,
				Timeout:   cfg.CommandTimeout(),
				Log:       log,
			}),
		})
	}

	mainPass := collect.NewMainPass(cfg.ClassifiedQueues, cfg.LabelCap)
	strandedPass := collect.NewStrandedPass(collect.StrandedConfig{MaxAge: cfg.StaleClaimAge()}, log)
	collector := collect.New(sched.RealClock{}, dbs, []collect.Pass{mainPass, collect.ThroughputPass{}, strandedPass}, log,
		collect.WithDownAfter(cfg.DownAfterConsecutiveFailures))
	mainPass.Init(ctx, dbs)

	reg := metrics.Default()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	srv := &http.Server{Handler: server.Handler(collector, reg, log), ReadHeaderTimeout: 10 * time.Second}

	done := make(chan string, 3)
	go func() {
		sched.Run(ctx, sched.RealClock{}, cfg.PollInterval(), func(c context.Context) { collector.RunPass(c, collect.PassMain) },
			func() { log.Warn("main pass overran its interval; skipping a tick") })
		done <- "main"
	}()
	go func() {
		sched.Run(ctx, sched.RealClock{}, cfg.StrandedInterval(), func(c context.Context) { collector.RunPass(c, collect.PassThroughput) },
			func() { log.Warn("throughput pass overran its interval; skipping a tick") })
		done <- "throughput"
	}()
	go func() {
		sched.Run(ctx, sched.RealClock{}, cfg.StrandedInterval(), func(c context.Context) { collector.RunPass(c, collect.PassStranded) },
			func() { log.Warn("stranded pass overran its interval; skipping a tick") })
		done <- "stranded"
	}()

	log.Info("beads-exporter started", "version", Version, "port", cfg.Port, "databases", len(dbs))
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	cancel()
	// Detach from ctx: it is already cancelled here, and the drain needs its own budget.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
	if err := joinLoops(done, []string{"main", "throughput", "stranded"}, joinTimeout, log); err != nil {
		return err
	}
	return serveErr
}

// joinTimeout bounds the wait for the pass loops after cancellation. With the
// 5 s HTTP drain it keeps shutdown near 10 s, inside the launchd ExitTimeOut
// of 15 s set by the module.
const joinTimeout = 5 * time.Second

// joinLoops waits for every named loop to report on done, but no longer than
// timeout. On overrun it logs the loops still in flight and returns an error so
// the process exits non-zero instead of hanging until launchd kills it. A
// pass interrupted this way loses only its in-memory work: snapshots are
// swapped in atomically, so the next start recovers by collecting again.
func joinLoops(done <-chan string, names []string, timeout time.Duration, log *slog.Logger) error {
	pending := make(map[string]bool, len(names))
	for _, n := range names {
		pending[n] = true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for len(pending) > 0 {
		select {
		case n := <-done:
			delete(pending, n)
		case <-timer.C:
			inFlight := make([]string, 0, len(pending))
			for _, n := range names {
				if pending[n] {
					inFlight = append(inFlight, n)
				}
			}
			log.Error("shutdown_overrun", "phase", "join_passes", "in_flight", inFlight, "timeout", timeout.String())
			return fmt.Errorf("shutdown overran %s waiting for pass loops: %v", timeout, inFlight)
		}
	}
	return nil
}

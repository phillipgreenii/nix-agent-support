package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// defaultServeAddr is the loopback listen address of the serve command; the
// port is the one reserved for this exporter in the shared port ledger.
const defaultServeAddr = "127.0.0.1:9148"

// buildInfoExposition is the only series served until the manager's metrics
// land.
func buildInfoExposition() string {
	return fmt.Sprintf("# HELP pg_gwm_build_info Build information; always 1.\n# TYPE pg_gwm_build_info gauge\npg_gwm_build_info{version=%q} 1\n", Version)
}

func (a *app) metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, buildInfoExposition())
	})
	return mux
}

// checkLoopback rejects a listen address whose host is not a loopback IP: the
// exposition is unauthenticated and MUST NOT leave the machine.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q is not a loopback IP", addr)
	}
	return nil
}

// serve runs the metrics HTTP listener until interrupted. The ready function,
// when set, receives the bound address (tests use it with port 0).
func (a *app) serve(args []string, ready func(addr string)) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	addr := fs.String("addr", defaultServeAddr, "loopback listen address host:port")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := checkLoopback(*addr); err != nil {
		fmt.Fprintf(a.stderr, "pg-gwm: serve: %v\n", err)
		return exitUsage
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", *addr)
	if err != nil {
		return a.fail(err)
	}
	srv := &http.Server{Handler: a.metricsHandler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Opt-in profiling endpoint (PG_GWM_PPROF_ADDR; serve only).
	defer startPprofFromEnv(os.Getenv, a.stderr)()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	fmt.Fprintf(a.stderr, "pg-gwm: serve: listening on %s\n", ln.Addr())
	if ready != nil {
		ready(ln.Addr().String())
	}
	select {
	case err := <-done:
		return a.fail(err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return a.fail(err)
	}
	return 0
}

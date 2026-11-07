package main

import (
	"context"
	"errors"
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

// metricsHandler serves the exposition of the pool at root, rendered afresh on
// every scrape so the scrape drives the refresh.
func (a *app) metricsHandler(root string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		text, err := a.renderExposition(root)
		if err != nil {
			fmt.Fprintf(a.stderr, "pg-checkout-pool: serve: %v\n", err)
			http.Error(w, "pool status unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, text)
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
	fs, root := a.newFlags("serve")
	addr := fs.String("addr", defaultServeAddr, "loopback listen address host:port")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if err := checkLoopback(*addr); err != nil {
		fmt.Fprintf(a.stderr, "pg-checkout-pool: serve: %v\n", err)
		return exitUsage
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return a.fail(err)
	}
	srv := &http.Server{Handler: a.metricsHandler(*root), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Opt-in profiling endpoint (PG_CHECKOUT_POOL_PPROF_ADDR; serve only).
	// Reports on stderr and never stops the daemon.
	defer startPprofFromEnv(os.Getenv, a.stderr)()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	fmt.Fprintf(a.stderr, "pg-checkout-pool: serve: listening on %s, root %s\n", ln.Addr(), *root)
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

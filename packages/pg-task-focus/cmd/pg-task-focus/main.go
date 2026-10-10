// Command pg-task-focus keeps its operator on a written daily routine: the
// daemon ("serve") and its command-line client. See `pg-task-focus --help`.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/cli"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/daemon"
)

// Version is the build version, stamped by the Nix builder (-X main.Version).
var Version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Execute(ctx, os.Args[1:], cli.App{Version: Version, Serve: serve}))
}

// serve runs the daemon until ctx ends. SIGHUP reloads the configuration.
func serve(ctx context.Context, p cli.ServeParams) error {
	d, err := daemon.Start(ctx, daemon.Params{
		ConfigPath: p.ConfigPath, DataDir: p.DataDir, Version: Version, Log: os.Stdout,
	})
	if err != nil {
		return err
	}
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	for {
		select {
		case <-hup:
			_ = d.Reload() // logged, counted and reported through /healthz
		case <-d.Done():
			return nil
		}
	}
}

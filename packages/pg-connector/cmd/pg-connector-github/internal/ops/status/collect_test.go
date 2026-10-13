package status

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/socket"
)

// shortDir is a temp directory whose path is short enough for a unix socket.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pcs")
	if err != nil {
		d, err = os.MkdirTemp("", "pcs")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// serveDaemon serves rt on a fresh socket until the test ends or stop is
// called, and returns the socket path and stop.
func serveDaemon(t *testing.T, rt *router.Router, build string) (sock string, stop func()) {
	t.Helper()
	dir := shortDir(t)
	sock = filepath.Join(dir, "s")
	srv, err := proc.Start(proc.Config{StateDir: dir, SocketPath: sock, DrainTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, socket.Handler(rt, socket.ServerConfig{Build: build}), nil)
	}()
	var once bool
	stop = func() {
		if !once {
			once = true
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return sock, stop
}

func collector(sock string, s Static) Collector {
	s.SocketPath = sock
	return Collector{Static: s, Budget: 1500 * time.Millisecond, Now: func() time.Time { return t0 }}
}

func TestCollectFromALiveDaemon(t *testing.T) {
	cfg, _ := testConfig(t)
	rt := router.New(router.Options{Instances: []string{"pr", "ci"}})
	if err := Register(rt, cfg); err != nil {
		t.Fatal(err)
	}
	sock, _ := serveDaemon(t, rt, cfg.BuildID)

	c := collector(sock, cfg.Static)
	r := c.Collect(context.Background())
	if r.Source != SourceDaemon || r.Health != Healthy || r.Health.ExitCode() != 0 {
		t.Fatalf("%+v", r)
	}
	if r.ClientBuildID != cfg.BuildID || r.BuildDiffers {
		t.Errorf("same build: client %q differs %t", r.ClientBuildID, r.BuildDiffers)
	}

	// A client of another build reports the difference, and it is not an error.
	c.Static.BuildID = "an-older-build"
	r = c.Collect(context.Background())
	if r.Health != Healthy || !r.BuildDiffers || r.ClientBuildID != "an-older-build" || r.BuildID != cfg.BuildID {
		t.Errorf("build difference: %+v", r)
	}

	// A degraded daemon is a report with exit code 2.
	paused := healthyBudgets()
	paused.Buckets[0].Paused, paused.Buckets[0].PauseReason, paused.Buckets[0].PausedUntil = true, "secondary rate limit", t0.Add(time.Minute)
	cfg2, _ := testConfig(t)
	cfg2.Budgets = fakeBudgets{paused}
	rt2 := router.New(router.Options{Instances: []string{"pr", "ci"}})
	if err := Register(rt2, cfg2); err != nil {
		t.Fatal(err)
	}
	sock2, _ := serveDaemon(t, rt2, cfg2.BuildID)
	if r := collector(sock2, cfg2.Static).Collect(context.Background()); r.Health != Degraded || r.Health.ExitCode() != 2 || r.Source != SourceDaemon {
		t.Errorf("%+v", r)
	}
}

func TestCollectWithTheDaemonDownIsTheLocalReport(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  string
		probe proc.SupervisorState
		want  string
	}{
		{"launchd", KindLaunchd, proc.SupervisorNotRegistered, "not_registered"},
		{"none", KindNone, proc.SupervisorNone, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testStatic(tc.probe, tc.kind)
			sock := filepath.Join(shortDir(t), "absent")
			start := time.Now()
			r := collector(sock, s).Collect(context.Background())
			if r.Health != Down || r.Health.ExitCode() != 3 || r.Source != SourceLocal {
				t.Fatalf("%+v", r)
			}
			if r.Supervisor.Kind != tc.kind || r.Supervisor.State != tc.want || r.Socket != sock {
				t.Errorf("supervisor %+v socket %q", r.Supervisor, r.Socket)
			}
			if len(r.Reasons) != 1 || !strings.Contains(r.Reasons[0], sock) {
				t.Errorf("the reason names the socket: %v", r.Reasons)
			}
			if took := time.Since(start); took > 4*time.Second {
				t.Errorf("a down daemon is retried for half the budget only, took %v", took)
			}
		})
	}
}

func TestCollectWhileTheDaemonMigratesIsDegraded(t *testing.T) {
	cfg, _ := testConfig(t)
	rt := router.New(router.Options{Instances: []string{"pr", "ci"}})
	if err := Register(rt, cfg); err != nil {
		t.Fatal(err)
	}
	rt.SetMigrating(true, "store schema 2 to 3")
	sock, _ := serveDaemon(t, rt, cfg.BuildID)
	r := collector(sock, cfg.Static).Collect(context.Background())
	if r.Health != Degraded || r.Source != SourceLocal || len(r.Reasons) != 1 || !strings.Contains(r.Reasons[0], "migrating:") {
		t.Errorf("%+v", r)
	}
}

func TestCollectOutsideTheProtocolWindowIsDegraded(t *testing.T) {
	cfg, _ := testConfig(t)
	rt := router.New(router.Options{Instances: []string{"pr", "ci"}})
	if err := Register(rt, cfg); err != nil {
		t.Fatal(err)
	}
	dir := shortDir(t)
	sock := filepath.Join(dir, "s")
	srv, err := proc.Start(proc.Config{StateDir: dir, SocketPath: sock, DrainTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, socket.Handler(rt, socket.ServerConfig{Build: "far-ahead", Protocol: 9}), nil)
	}()
	t.Cleanup(func() { cancel(); <-done })

	r := collector(sock, cfg.Static).Collect(context.Background())
	if r.Health != Degraded || len(r.Reasons) != 1 || !strings.Contains(r.Reasons[0], "far-ahead") {
		t.Errorf("%+v", r)
	}
}

package serve

import (
	"database/sql"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/server"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"
)

// hello answers from cached state: it must not wait behind a long write
// transaction. If it went back to the write connection this test would block
// until the transaction ended and fail on the timeout.
func TestHelloAnswersWhileTheWriteConnectionIsHeld(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	defer func() { _ = d.Close() }()
	d.srv = server.New(d.obs, d.cfg.Clients, d.hello)
	d.registerOps()

	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- d.st.WithWrite("", func(*sql.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the write transaction never started")
	}

	answered := make(chan wire.Hello, 1)
	go func() { answered <- d.hello().(wire.Hello) }()
	select {
	case h := <-answered:
		if h.SchemaVersion < 1 || h.Epoch < 1 {
			t.Errorf("hello %+v", h)
		}
	case <-time.After(5 * time.Second):
		t.Error("hello did not answer while the write connection was held")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A rebuild bumps the store epoch, and hello reports the new one.
func TestHelloReportsTheEpochAfterARebuild(t *testing.T) {
	e := newEnv(t, false)
	d := e.open(t)
	d.srv = server.New(d.obs, d.cfg.Clients, d.hello)
	before := d.hello().(wire.Hello).Epoch
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d2, err := Open(Options{Config: e.cfg, Logger: NewLogger(e.log), NoEvents: true, Version: "test", Procs: e.procs, Rebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d2.Close() }()
	d2.srv = server.New(d2.obs, d2.cfg.Clients, d2.hello)
	if after := d2.hello().(wire.Hello).Epoch; after != before+1 {
		t.Fatalf("epoch after rebuild %d, want %d", after, before+1)
	}
}

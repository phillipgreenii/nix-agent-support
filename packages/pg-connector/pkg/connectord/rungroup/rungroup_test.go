package rungroup

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/phillipgreenii/x/lifecycle"
)

// fakeSignal returns a Notify that lets the test deliver "the first signal".
func fakeSignal() (func(context.Context, ...os.Signal) (context.Context, context.CancelFunc), context.CancelFunc) {
	var cancel context.CancelFunc
	notify := func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		var ctx context.Context
		ctx, cancel = context.WithCancel(parent)
		return ctx, cancel
	}
	return notify, func() { cancel() }
}

func TestParseArgs(t *testing.T) {
	for _, c := range []struct {
		args    []string
		want    time.Duration
		wantErr bool
	}{
		{nil, DefaultDeadline, false},
		{[]string{"--shutdown-deadline", "12s"}, 12 * time.Second, false},
		{[]string{"--shutdown-deadline=3s"}, 3 * time.Second, false},
		{[]string{"--shutdown-deadline", "0s"}, 0, true},
		{[]string{"--shutdown-deadline", "soon"}, 0, true},
		{[]string{"--nope"}, 0, true},
		{[]string{"extra"}, 0, true},
	} {
		got, err := ParseArgs("serve", c.args)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("ParseArgs(%v) = %v, %v; want %v, err=%v", c.args, got, err, c.want, c.wantErr)
		}
	}
}

// A SIGTERM that lands while startup is still running (and startup ignores
// its context) must still end the process within the deadline, naming the
// phase it hung in.
func TestSignalDuringStartupHardExitsAtTheDeadline(t *testing.T) {
	notify, signal := fakeSignal()
	exited := make(chan int, 1)
	phase := make(chan string, 1)
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})
	go Run(context.Background(), lifecycle.Options{
		Deadline: 50 * time.Millisecond,
		Notify:   notify,
		Exit:     func(code int) { exited <- code },
		OnEvent: func(ev lifecycle.Event) {
			if ev.Name == lifecycle.EvOverrun {
				phase <- ev.Phase
			}
		},
	}, func(ctx context.Context, setPhase func(string)) int {
		setPhase("opening_store")
		close(started)
		<-release // startup that never looks at ctx
		return 0
	})
	<-started
	signal()
	select {
	case code := <-exited:
		if code != ExitOverrun {
			t.Errorf("exit code = %d, want %d", code, ExitOverrun)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the deadline did not end a shutdown that began during startup")
	}
	if got := <-phase; got != "opening_store" {
		t.Errorf("overrun phase = %q, want opening_store", got)
	}
}

// A daemon that honours the context returns its code and the watchdog never
// fires.
func TestCleanShutdownReturnsTheDaemonCodeWithoutExit(t *testing.T) {
	notify, signal := fakeSignal()
	got := make(chan int, 1)
	running := make(chan struct{})
	go func() {
		got <- Run(context.Background(), lifecycle.Options{
			Deadline: time.Minute,
			Notify:   notify,
			Exit:     func(int) { t.Error("hard exit on a clean shutdown") },
		}, func(ctx context.Context, _ func(string)) int {
			close(running)
			<-ctx.Done()
			return 7
		})
	}()
	<-running
	signal()
	if code := <-got; code != 7 {
		t.Errorf("code = %d, want 7", code)
	}
}

package core

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// The notice is the whole operator surface of the soft step (it is not a gate), so
// each non-ok state must say plainly what is HALTED, what still RUNS, why, and the
// remedies; ok renders nothing.
func TestLogLimitNotice(t *testing.T) {
	const mib = 1 << 20
	base := LogLimitView{Bytes: 58 * mib, LimitBytes: 64 * mib, SoftBytes: 57*mib + 600*1024}
	for _, tc := range []struct {
		name  string
		v     LogLimitView
		wants []string
		nots  []string
	}{
		{"ok renders nothing", LogLimitView{State: eventqueue.StateOK, Bytes: 5, LimitBytes: 10}, nil, nil},
		{"zero value renders nothing", LogLimitView{}, nil, nil},
		{
			"emitters halted",
			withState(base, eventqueue.StateEmittersHalted),
			[]string{"LOG LIMIT", "58.0 MiB of 64.0 MiB (91%)", "soft threshold", "HALTED: polled command-source emitters", "STILL RUNNING: listener dispatch and drain, timer emitters, and pushed events", "Only the hard limit stops those", "PG_ROUTER_MAX_LOG_BYTES", "restart pg-router", "move queue.jsonl aside"},
			[]string{"log_full", "rejected"},
		},
		{
			"log full",
			func() LogLimitView { v := withState(base, eventqueue.StateLogFull); v.RejectedLogFull = 7; return v }(),
			[]string{"LOG FULL", "hard limit", "HALTED: polled command-source emitters, AND all new events", "rejected with `log_full` (7 rejected so far)", "STILL RUNNING: listener dispatch and drain", "PG_ROUTER_MAX_LOG_BYTES"},
			nil,
		},
		{
			"log unwritable",
			func() LogLimitView {
				v := withState(base, eventqueue.StateLogUnwritable)
				v.RejectedLogUnwritable = 3
				v.Detail = "no space left on device"
				return v
			}(),
			[]string{"LOG UNWRITABLE", "no space left on device", "rejected with `log_unwritable` (3 rejected so far)", "STILL RUNNING: listener dispatch", "Recovery is automatic", "disk space"},
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(LogLimitNotice(tc.v), "\n")
			if tc.wants == nil && got != "" {
				t.Fatalf("notice = %q, want none", got)
			}
			for _, w := range tc.wants {
				if !strings.Contains(got, w) {
					t.Errorf("notice lacks %q:\n%s", w, got)
				}
			}
			for _, n := range tc.nots {
				if strings.Contains(got, n) {
					t.Errorf("notice must not contain %q:\n%s", n, got)
				}
			}
		})
	}
}

func withState(v LogLimitView, s string) LogLimitView { v.State = s; return v }

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", -1: "0 B", 1: "1 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 33_282_518: "31.7 MiB", 64 << 20: "64.0 MiB", 3 << 30: "3.0 GiB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

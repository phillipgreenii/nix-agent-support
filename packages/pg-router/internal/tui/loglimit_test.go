package tui

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/internal/tui/render"
)

func limitLog(state string, pct float64) QueueLog {
	l := QueueLog{Bytes: int64(pct * float64(64<<20) / 100), LimitBytes: 64 << 20, SoftBytes: 60397977, Percent: pct, State: state}
	l.EmittersHalted = state != "ok" && state != ""
	return l
}

func TestQueueLogSummary_withLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		l    QueueLog
		want string
	}{
		{"no limit reported", QueueLog{Bytes: 33291620}, "log: 31.7 MiB"},
		{"healthy", limitLog("ok", 49), "log: 31.4 MiB / 64.0 MiB (49%)"},
		{"emitters halted", limitLog("emitters_halted", 93), "log: 59.5 MiB / 64.0 MiB (93%) · emitters halted"},
		{"full", limitLog("log_full", 100), "log: 64.0 MiB / 64.0 MiB (100%) · FULL, rejecting events"},
		{"unwritable", limitLog("log_unwritable", 12), "· UNWRITABLE, rejecting events"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := queueLogSummary(tc.l); !strings.Contains(got, tc.want) {
				t.Errorf("summary = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// The documented bands: plain below 70%, warn 70-<90, crit at/over 90% (the soft
// threshold) or whenever the state is not ok.
func TestLogBand(t *testing.T) {
	for _, tc := range []struct {
		l    QueueLog
		want string
	}{
		{QueueLog{Bytes: 1 << 40}, bandOK}, // no limit reported: nothing to measure against
		{limitLog("ok", 0), bandOK},
		{limitLog("ok", 69.9), bandOK},
		{limitLog("ok", 70), bandWarn},
		{limitLog("ok", 89.9), bandWarn},
		{limitLog("ok", 90), bandCrit},
		{limitLog("ok", 150), bandCrit},
		{limitLog("emitters_halted", 10), bandCrit}, // a non-ok state is crit whatever the percent
		{limitLog("log_unwritable", 1), bandCrit},
		{limitLog("", 10), bandOK}, // an older core: no state
	} {
		if got := logBand(tc.l); got != tc.want {
			t.Errorf("logBand(state=%q pct=%v) = %q, want %q", tc.l.State, tc.l.Percent, got, tc.want)
		}
	}
}

// The header shows the log against its limit in every tier.
func TestBanner_HeaderShowsLogPercent(t *testing.T) {
	theme := render.NewTheme(false)
	for _, width := range []int{120, 60} {
		got := renderTopZone(topZoneData{
			clientVersion: "1.2.3",
			reply: StatusReply{
				Core:     CoreInfo{State: "started", Version: "1.2.3"},
				QueueLog: limitLog("emitters_halted", 93),
			},
			width: width,
			theme: theme,
		})
		if !strings.Contains(got, "(93%)") {
			t.Errorf("width %d: header lacks the percent of limit; got:\n%s", width, got)
		}
	}
}

// The limited-capability zone says plainly what is HALTED, what still RUNS, why
// and the remedies — and is absent when the log is healthy.
func TestLogLimitZone(t *testing.T) {
	theme := render.NewTheme(false)
	if got := logLimitZone(limitLog("ok", 50), 100, theme); got != "" {
		t.Fatalf("healthy log rendered a zone: %q", got)
	}
	if got := logLimitZone(QueueLog{Bytes: 5}, 100, theme); got != "" {
		t.Fatalf("a core without limit fields rendered a zone: %q", got)
	}
	for _, tc := range []struct {
		name  string
		l     QueueLog
		wants []string
	}{
		{
			"halted", limitLog("emitters_halted", 93),
			[]string{"! LOG LIMIT", "HALTED: polled command-source emitters", "STILL RUNNING: listener dispatch and drain, timer emitters, and pushed events", "Remedies:", "PG_ROUTER_MAX_LOG_BYTES"},
		},
		{
			"full", func() QueueLog { l := limitLog("log_full", 100); l.Rejected.LogFull = 12; return l }(),
			[]string{"! LOG FULL", "rejected with `log_full` (12 rejected so far)", "STILL RUNNING"},
		},
		{
			"unwritable", func() QueueLog { l := limitLog("log_unwritable", 5); l.Detail = "no space left on device"; return l }(),
			[]string{"! LOG UNWRITABLE", "no space left on device", "Recovery is automatic"},
		},
	} {
		for _, width := range []int{120, 70, 40} {
			t.Run(tc.name, func(t *testing.T) {
				got := logLimitZone(tc.l, width, theme)
				for _, want := range tc.wants {
					// a phrase may wrap onto the next line at narrow widths
					if !strings.Contains(strings.Join(strings.Fields(got), " "), want) {
						t.Errorf("width %d: zone lacks %q:\n%s", width, want, got)
					}
				}
				for i, line := range strings.Split(got, "\n") {
					if n := len([]rune(line)); n > width+8 { // one over-long word may exceed
						t.Errorf("width %d: line %d is %d columns, not wrapped: %q", width, i, n, line)
					}
				}
			})
		}
	}
}

// renderMain includes the zone only when the log is not healthy.
func TestRenderMain_LogLimitZone(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain
	m.width, m.height = 120, 60
	m.reply = StatusReply{Core: CoreInfo{State: "started"}, QueueLog: limitLog("ok", 10)}
	if got := m.renderMain(); strings.Contains(got, "STILL RUNNING") {
		t.Fatalf("healthy log rendered the limited-capability zone:\n%s", got)
	}
	m.reply.QueueLog = limitLog("emitters_halted", 93)
	got := m.renderMain()
	for _, want := range []string{"HALTED: polled command-source emitters", "STILL RUNNING: listener dispatch"} {
		if !strings.Contains(got, want) {
			t.Fatalf("renderMain lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "PAUSED") {
		t.Fatalf("the soft step is not a gate: no PAUSED banner expected:\n%s", got)
	}
}

// Under height pressure the zone outlives the unfocused panes (drop order 4).
func TestRenderMain_LogLimitZoneSurvivesPaneDrops(t *testing.T) {
	m := newTestModel(nil)
	m.screen = screenMain
	m.width, m.height = 100, 14
	m.reply = StatusReply{
		Core:      CoreInfo{State: "started"},
		Listeners: []Listener{{Role: "feedback"}},
		Sources:   []Source{{Name: "queue-src"}},
		Queues:    []Queue{{Type: "pr.review"}},
		QueueLog:  limitLog("log_full", 100),
	}
	if got := m.renderMain(); !strings.Contains(got, "LOG FULL") {
		t.Fatalf("the log-full notice was dropped under height pressure:\n%s", got)
	}
}

func TestRenderProblemsModal_LogLimit(t *testing.T) {
	m := newTestModel(nil)
	m.width, m.height = 120, 60
	m.reply = StatusReply{QueueLog: limitLog("ok", 10)}
	if got := m.renderProblemsModal(); !strings.Contains(got, "log limit") {
		t.Fatalf("healthy: the Problems modal should state the log limit is ok:\n%s", got)
	}
	m.reply = StatusReply{QueueLog: limitLog("emitters_halted", 93)}
	got := m.renderProblemsModal()
	for _, want := range []string{"log limit", "HALTED: polled command-source emitters", "STILL RUNNING"} {
		if !strings.Contains(got, want) {
			t.Errorf("Problems modal lacks %q:\n%s", want, got)
		}
	}
}

func TestWrapWords(t *testing.T) {
	for _, tc := range []struct {
		in    string
		width int
		want  []string
	}{
		{"short", 20, []string{"short"}},
		{"aaa bbb ccc ddd", 7, []string{"aaa bbb", "  ccc", "  ddd"}},
		{"aaa bbb", 0, []string{"aaa bbb"}},
		{"supercalifragilistic word", 5, []string{"supercalifragilistic", "  word"}},
	} {
		got := wrapWords(tc.in, tc.width, "  ")
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("wrapWords(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
		}
	}
}

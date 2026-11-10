package originguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readEvents(t *testing.T, dir string) []map[string]any {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	var out []map[string]any
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("bad line %q: %v", line, err)
			}
			out = append(out, m)
		}
	}
	return out
}

func TestFileSinkWritesDailyFile(t *testing.T) {
	dir := t.TempDir()
	clock := newClock()
	s := &FileSink{Dir: dir, Clock: clock, RetentionDays: 14}
	a, tr := 0, 1
	if err := s.Emit(Event{Event: EventGuardState, Realm: "zr", Available: &a, Transition: &tr, Reason: "cert-expired"}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(24 * time.Hour)
	if err := s.Emit(Event{Event: EventGuardFault, Realm: "zr", Fault: "state-write"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"events-2026-11-10.jsonl", "events-2026-11-11.jsonl"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: err=%v mode=%v", name, err, fi)
		}
	}
	evs := readEvents(t, dir)
	if len(evs) != 2 || evs[0]["available"] != float64(0) || evs[0]["reason"] != "cert-expired" || evs[0]["ts"] == "" {
		t.Errorf("events = %v", evs)
	}
}

func TestFileSinkUsesUTCDate(t *testing.T) {
	dir := t.TempDir()
	est := time.FixedZone("EST", -5*3600)
	late := time.Date(2026, 11, 10, 22, 30, 0, 0, est) // 03:30 UTC on the 11th
	s := &FileSink{Dir: dir, Clock: fixedClock{late}, RetentionDays: 14}
	if err := s.Emit(Event{Event: EventGuardFault, Realm: "zr"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events-2026-11-11.jsonl")); err != nil {
		t.Fatal(err)
	}
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func TestFileSinkPrunesByDateOnFirstWriteOfDay(t *testing.T) {
	dir := t.TempDir()
	clock := newClock() // 2026-11-10, retention 14 days: cutoff 2026-10-27
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{
		"events-2026-10-01.jsonl", "events-2026-10-26.jsonl", // older: deleted
		"events-2026-10-27.jsonl", "events-2026-11-09.jsonl", // kept
		"events-notadate.jsonl", "other-2026-01-01.jsonl", "events-2026-01-01.txt", // never touched
	} {
		touch(n)
	}
	s := &FileSink{Dir: dir, Clock: clock, RetentionDays: 14}
	if err := s.Emit(Event{Event: EventGuardFault, Realm: "zr"}); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[string]bool{
		"events-2026-10-01.jsonl": false, "events-2026-10-26.jsonl": false,
		"events-2026-10-27.jsonl": true, "events-2026-11-09.jsonl": true,
		"events-notadate.jsonl": true, "other-2026-01-01.jsonl": true, "events-2026-01-01.txt": true,
		"events-2026-11-10.jsonl": true,
	} {
		_, err := os.Stat(filepath.Join(dir, n))
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", n, err == nil, want)
		}
	}

	// A second write the same day does not prune again.
	touch("events-2026-09-01.jsonl")
	if err := s.Emit(Event{Event: EventGuardFault, Realm: "zr"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events-2026-09-01.jsonl")); err != nil {
		t.Error("prune must run only on the first write of a day")
	}
}

func TestFileSinkPruneFailureDoesNotFailWrite(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory with an old events name cannot be removed by os.Remove.
	old := filepath.Join(dir, "events-2020-01-01.jsonl")
	if err := os.MkdirAll(filepath.Join(old, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &FileSink{Dir: dir, Clock: newClock(), RetentionDays: 14}
	if err := s.Emit(Event{Event: EventGuardFault, Realm: "zr"}); err != nil {
		t.Fatalf("a prune failure failed the write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "events-2026-11-10.jsonl")); err != nil {
		t.Error("event not written")
	}
}

func newRecorder(t *testing.T) (*Recorder, *captureSink, *fakeClock) {
	t.Helper()
	clock := newClock()
	sink := &captureSink{}
	store := NewStore(filepath.Join(t.TempDir(), "state"), clock, 0)
	return &Recorder{Store: store, Sink: sink, Clock: clock, HeartbeatInterval: 5 * time.Minute}, sink, clock
}

func TestAvailableAndStayingAvailableEmitsNothing(t *testing.T) {
	r, sink, clock := newRecorder(t)
	for range 5 {
		if _, err := r.Record("zr", Observation{Reason: ReasonOK}); err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Hour)
	}
	if n := len(sink.list()); n != 0 {
		t.Errorf("%d events while available, want 0", n)
	}
}

func TestTransitionsAndHeartbeatTiming(t *testing.T) {
	r, sink, clock := newRecorder(t)
	rec := func(reason Reason) {
		t.Helper()
		if _, err := r.Record("zr", Observation{Reason: reason}); err != nil {
			t.Fatal(err)
		}
	}
	rec(ReasonCertExpired) // transition 1
	if evs := sink.list(); len(evs) != 1 || *evs[0].Transition != 1 || *evs[0].Available != 0 || evs[0].Reason != "cert-expired" {
		t.Fatalf("after first: %+v", evs)
	}
	clock.Advance(4*time.Minute + 59*time.Second)
	rec(ReasonCertExpired) // too soon for a heartbeat
	if n := len(sink.list()); n != 1 {
		t.Fatalf("%d events before the interval", n)
	}
	clock.Advance(time.Second) // exactly 5 minutes since the transition
	rec(ReasonCertExpired)
	evs := sink.list()
	if len(evs) != 2 || *evs[1].Transition != 0 || *evs[1].UnavailableSeconds != 300 {
		t.Fatalf("heartbeat: %+v", evs)
	}
	clock.Advance(time.Minute)
	rec(ReasonCertExpired) // heartbeat restarts the interval
	if n := len(sink.list()); n != 2 {
		t.Fatalf("%d events, want 2", n)
	}
	rec(ReasonNetwork) // reason change is a transition even while unavailable
	if evs := sink.list(); len(evs) != 3 || *evs[2].Transition != 1 || evs[2].Reason != "network" {
		t.Fatalf("reason change: %+v", evs)
	}
	clock.Advance(time.Minute)
	rec(ReasonOK) // recovery
	evs = sink.list()
	if len(evs) != 4 || *evs[3].Transition != 1 || *evs[3].Available != 1 || evs[3].UnavailableSeconds != nil {
		t.Fatalf("recovery: %+v", evs)
	}
	clock.Advance(time.Hour)
	rec(ReasonOK)
	if n := len(sink.list()); n != 4 {
		t.Fatalf("%d events after recovery, want 4", n)
	}
}

func TestSinceKeepsFirstUnavailableMoment(t *testing.T) {
	r, _, clock := newRecorder(t)
	first, _ := r.Record("zr", Observation{Reason: ReasonCertExpired})
	clock.Advance(time.Hour)
	later, _ := r.Record("zr", Observation{Reason: ReasonNetwork})
	if !later.Since.Equal(first.Since) || !first.Since.Equal(t0) {
		t.Errorf("since moved: %v then %v", first.Since, later.Since)
	}
	clock.Advance(time.Hour)
	ok, _ := r.Record("zr", Observation{Reason: ReasonOK})
	if !ok.Since.IsZero() {
		t.Errorf("since = %v after recovery", ok.Since)
	}
}

func TestLoginAttemptOutcomeIsEmitted(t *testing.T) {
	r, sink, _ := newRecorder(t)
	if _, err := r.Record("zr", Observation{Reason: ReasonOK, LoginAttempt: "ok"}); err != nil {
		t.Fatal(err)
	}
	evs := sink.list()
	if len(evs) != 1 || evs[0].LoginAttempt != "ok" || *evs[0].Transition != 0 {
		t.Fatalf("events = %+v", evs)
	}
}

func TestRecordRedactsStoredError(t *testing.T) {
	r, _, _ := newRecorder(t)
	st, err := r.Record("zr", Observation{
		Reason:    ReasonAuthRejected,
		LastError: "visit https://idp.example/auth?state=s3cr3t&code=0042 to sign in",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.LastError, "s3cr3t") || strings.Contains(st.LastError, "0042") {
		t.Errorf("LastError leaked: %q", st.LastError)
	}
}

func TestEventsCarryNoHostUserOrText(t *testing.T) {
	dir := t.TempDir()
	clock := newClock()
	store := NewStore(filepath.Join(t.TempDir(), "state"), clock, 0)
	r := &Recorder{Store: store, Sink: &FileSink{Dir: dir, Clock: clock, RetentionDays: 14}, Clock: clock, HeartbeatInterval: time.Minute}
	if _, err := r.Record("zr", Observation{Reason: ReasonNetwork, LastError: "ssh: connect to host github.com port 22: org-bob@secret"}); err != nil {
		t.Fatal(err)
	}
	_ = r.Fault("zr", "config")
	data, _ := os.ReadFile(filepath.Join(dir, "events-2026-11-10.jsonl"))
	for _, leak := range []string{"github.com", "org-bob", "secret", "ssh:"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("event line leaked %q: %s", leak, data)
		}
	}
	for _, m := range readEvents(t, dir) {
		for k := range m {
			switch k {
			case "ts", "event", "realm", "available", "reason", "transition", "login_attempt", "unavailable_s", "fault":
			default:
				t.Errorf("unexpected event field %q", k)
			}
		}
	}
}

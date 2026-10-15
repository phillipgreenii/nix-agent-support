package lease

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validShim() *Lease {
	return &Lease{
		Owner: "shim", Key: "4412-1760400000123456", Kind: KindShim,
		Client: &ProcRef{PID: 4412, StartUS: 1760400000123456},
		Flags:  []string{"i", "s"}, Argv: []string{"caffeinate", "-i", "-s"},
	}
}

func TestLeaseValidate(t *testing.T) {
	l := validShim()
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	if l.ID != "shim.4412-1760400000123456" || l.SchemaVersion != SchemaVersion {
		t.Errorf("Validate did not fill id/schema: %+v", l)
	}

	bad := map[string]func(*Lease){
		"owner with dot":      func(l *Lease) { l.Owner = "a.b" },
		"empty owner":         func(l *Lease) { l.Owner = "" },
		"key with slash":      func(l *Lease) { l.Key = "../x" },
		"key too long":        func(l *Lease) { l.Key = strings.Repeat("k", 65) },
		"id mismatch":         func(l *Lease) { l.ID = "shim.other" },
		"bad kind":            func(l *Lease) { l.Kind = "daemon" },
		"shim with bad owner": func(l *Lease) { l.Owner = "tool" },
		"unbounded and unowned": func(l *Lease) {
			l.Client, l.ExpiresAt, l.Watch = nil, nil, nil
		},
		"bad flag":    func(l *Lease) { l.Flags = []string{"x"} },
		"zero client": func(l *Lease) { l.Client = &ProcRef{} },
	}
	for name, mut := range bad {
		l := validShim()
		mut(l)
		if err := l.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, l)
		}
	}

	// A pure timer (acquire --for) has no client but is bounded.
	timer := &Lease{Owner: "alice", Key: "k1", Kind: KindAcquire, ExpiresAt: timePtr(time.Now().Add(time.Hour))}
	if err := timer.Validate(); err != nil {
		t.Errorf("pure timer lease refused: %v", err)
	}
	// 64-char parts are the maximum.
	max := &Lease{Owner: strings.Repeat("o", 64), Key: strings.Repeat("k", 64), Kind: KindRun, Watch: &ProcRef{PID: 5, StartUS: 5}}
	if err := max.Validate(); err != nil {
		t.Errorf("64-char owner/key refused: %v", err)
	}
}

func TestLeaseLive(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	past, future := now.Add(-time.Second), now.Add(time.Hour)
	p := fakeProber{
		10: {Exists: true, StartUS: 100},
		11: {Exists: true, StartUS: 100, Zombie: true},
		12: {Exists: true, StartUS: 999},
	}
	cases := []struct {
		name string
		l    Lease
		want bool
	}{
		{"client alive", Lease{Client: &ProcRef{10, 100}}, true},
		{"client missing", Lease{Client: &ProcRef{13, 100}}, false},
		{"client start mismatch (pid reuse)", Lease{Client: &ProcRef{12, 100}}, false},
		{"client zombie", Lease{Client: &ProcRef{11, 100}}, false},
		{"timer in future", Lease{ExpiresAt: &future}, true},
		{"timer in past", Lease{ExpiresAt: &past}, false},
		{"timer exactly now is expired", Lease{ExpiresAt: &now}, false},
		{"client alive but expired", Lease{Client: &ProcRef{10, 100}, ExpiresAt: &past}, false},
		{"client dead but unexpired", Lease{Client: &ProcRef{13, 100}, ExpiresAt: &future}, false},
		{"watch alive", Lease{ExpiresAt: &future, Watch: &ProcRef{10, 100}}, true},
		{"watch missing", Lease{ExpiresAt: &future, Watch: &ProcRef{13, 100}}, false},
		{"watch start mismatch", Lease{ExpiresAt: &future, Watch: &ProcRef{12, 100}}, false},
		{"all three satisfied", Lease{Client: &ProcRef{10, 100}, ExpiresAt: &future, Watch: &ProcRef{10, 100}}, true},
	}
	for _, c := range cases {
		if got := c.l.Live(now, p); got != c.want {
			t.Errorf("%s: Live = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPauseExpired(t *testing.T) {
	now := time.Now()
	if (&PauseFile{}).Expired(now) {
		t.Error("a pause with no until never expires")
	}
	if !(&PauseFile{Until: timePtr(now.Add(-time.Second))}).Expired(now) {
		t.Error("past until must be expired")
	}
	if (&PauseFile{Until: timePtr(now.Add(time.Minute))}).Expired(now) {
		t.Error("future until must not be expired")
	}
}

// The status.json type carries the additive fields and documents them; this
// pins their JSON names, which the keeper-side and status packets rely on.
func TestStatusAdditiveFieldNames(t *testing.T) {
	s := StatusFile{HeartbeatIntervalS: 10, UnexplainedTicks: 2, LingerUntil: timePtr(time.Unix(1, 0).UTC())}
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"heartbeat_interval_s", "unexplained_ticks", "linger_until"} {
		if _, ok := m[k]; !ok {
			t.Errorf("status.json missing key %q in %s", k, b)
		}
	}
}

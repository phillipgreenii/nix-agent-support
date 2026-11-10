package originguard

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type presenceRig struct {
	dir   string
	clock *fakeClock
	fp    *FilePresence
}

func newPresenceRig(t *testing.T) *presenceRig {
	t.Helper()
	dir := t.TempDir()
	clock := newClock()
	return &presenceRig{dir: dir, clock: clock, fp: &FilePresence{
		Path: filepath.Join(dir, "userpresence.json"), Clock: clock,
		MaxAge: 60 * time.Second, RefreshSeconds: 30,
	}}
}

func (r *presenceRig) write(t *testing.T, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(r.fp.Path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(r.fp.Path, mode); err != nil {
		t.Fatal(err)
	}
}

func (r *presenceRig) doc(state string, age time.Duration) string {
	return fmt.Sprintf(`{"schemaVersion":1,"state":%q,"idleSeconds":1,"since":"2026-11-10T11:00:00Z","sampledAt":"2026-11-10T11:59:00Z","writtenAt":%q}`+"\n",
		state, r.clock.Now().Add(-age).Format(time.RFC3339))
}

func TestPresenceStates(t *testing.T) {
	cases := []struct {
		state   string
		want    PresenceState
		noHuman bool
	}{
		{"asleep", PresenceAsleep, true},
		{"away", PresenceAway, true},
		{"present", PresencePresent, false},
		{"idle", PresenceIdle, false},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			r := newPresenceRig(t)
			r.write(t, r.doc(c.state, 5*time.Second), 0o600)
			p := r.fp.Read()
			if p.State != c.want || p.NoHuman() != c.noHuman {
				t.Fatalf("got %+v noHuman=%v", p, p.NoHuman())
			}
		})
	}
}

func TestPresenceUnknownCasesAssumeHuman(t *testing.T) {
	type tc struct {
		name  string
		setup func(t *testing.T, r *presenceRig)
		why   string
	}
	cases := []tc{
		{"missing", func(*testing.T, *presenceRig) {}, "missing"},
		{"stale", func(t *testing.T, r *presenceRig) { r.write(t, r.doc("asleep", 61*time.Second), 0o600) }, "stale"},
		{"unparsable", func(t *testing.T, r *presenceRig) { r.write(t, "{nope", 0o600) }, "unparsable"},
		{"empty", func(t *testing.T, r *presenceRig) { r.write(t, "", 0o600) }, "unparsable"},
		{"unknown schema", func(t *testing.T, r *presenceRig) {
			r.write(t, `{"schemaVersion":2,"state":"asleep","writtenAt":"2026-11-10T12:00:00Z"}`, 0o600)
		}, "unknown schema version"},
		{"unknown state", func(t *testing.T, r *presenceRig) { r.write(t, r.doc("dozing", 0), 0o600) }, "unknown state"},
		{"bad writtenAt", func(t *testing.T, r *presenceRig) {
			r.write(t, `{"schemaVersion":1,"state":"asleep","writtenAt":"yesterday"}`, 0o600)
		}, "unparsable writtenAt"},
		{"future", func(t *testing.T, r *presenceRig) { r.write(t, r.doc("asleep", -10*time.Minute), 0o600) }, "written in the future"},
		{"group writable", func(t *testing.T, r *presenceRig) { r.write(t, r.doc("asleep", 0), 0o620) }, "group or world writable"},
		{"world writable", func(t *testing.T, r *presenceRig) { r.write(t, r.doc("asleep", 0), 0o602) }, "group or world writable"},
		{"symlink", func(t *testing.T, r *presenceRig) {
			real := filepath.Join(r.dir, "real.json")
			if err := os.WriteFile(real, []byte(r.doc("asleep", 0)), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, r.fp.Path); err != nil {
				t.Fatal(err)
			}
		}, "not a regular file"},
		{"directory", func(t *testing.T, r *presenceRig) {
			if err := os.Mkdir(r.fp.Path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "not a regular file"},
		{"foreign owner", func(t *testing.T, r *presenceRig) {
			r.write(t, r.doc("asleep", 0), 0o600)
			r.fp.UID = func() uint32 { return uint32(os.Getuid()) + 1 }
		}, "foreign owner"},
		{"oversized", func(t *testing.T, r *presenceRig) {
			r.write(t, r.doc("asleep", 0)+string(make([]byte, 5000)), 0o600)
		}, "unreadable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newPresenceRig(t)
			c.setup(t, r)
			p := r.fp.Read()
			if p.State != PresenceUnknown || p.NoHuman() {
				t.Fatalf("got %+v, want unknown (assume human)", p)
			}
			if p.Why != c.why {
				t.Errorf("why = %q, want %q", p.Why, c.why)
			}
		})
	}
}

func TestPresenceAgeRule(t *testing.T) {
	r := newPresenceRig(t)
	if r.fp.Limit() != 60*time.Second {
		t.Fatalf("limit = %v, want max(60s, 2x30s)", r.fp.Limit())
	}
	r.write(t, r.doc("asleep", 60*time.Second), 0o600)
	if r.fp.Read().State != PresenceAsleep {
		t.Error("an exactly-limit age is still fresh")
	}
	r.fp.RefreshSeconds = 120 // 2 x refresh = 240 s beats max_age
	r.write(t, r.doc("asleep", 200*time.Second), 0o600)
	if r.fp.Limit() != 240*time.Second || r.fp.Read().State != PresenceAsleep {
		t.Errorf("limit = %v, state = %v", r.fp.Limit(), r.fp.Read().State)
	}
	r.clock.Advance(time.Minute) // age 260 s
	if r.fp.Read().State != PresenceUnknown {
		t.Error("a file older than the limit must be unknown")
	}
}

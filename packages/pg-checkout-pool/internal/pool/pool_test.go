package pool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestStore(t *testing.T, slots ...string) (*Store, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	s := NewStore(t.TempDir(), clk)
	n := 0
	s.newID = func() string { n++; return fmt.Sprintf("lease-%d", n) }
	for _, id := range slots {
		if err := s.CreateSlot(id); err != nil {
			t.Fatal(err)
		}
	}
	return s, clk
}

var allStates = []State{WarmFree, Leased, Resetting, Quarantined, Retired}

func TestTransitionTable(t *testing.T) {
	legal := map[[2]State]bool{
		{WarmFree, Resetting}: true, {WarmFree, Quarantined}: true, {WarmFree, Retired}: true,
		{Resetting, Leased}: true, {Resetting, WarmFree}: true, {Resetting, Quarantined}: true,
		{Leased, Resetting}: true, {Leased, Quarantined}: true,
		{Quarantined, Resetting}: true, {Quarantined, Retired}: true,
	}
	for _, from := range allStates {
		if !from.Valid() {
			t.Errorf("%s not valid", from)
		}
		for _, to := range allStates {
			if got := CanTransition(from, to); got != legal[[2]State{from, to}] {
				t.Errorf("CanTransition(%s,%s)=%v", from, to, got)
			}
		}
	}
	if State("bogus").Valid() {
		t.Error("bogus state valid")
	}
}

// drive puts a slot in the wanted state through legal moves and returns the handle.
func drive(t *testing.T, s *Store, id string, want State) *Handle {
	t.Helper()
	h, err := s.Lock(id)
	if err != nil {
		t.Fatal(err)
	}
	steps := map[State][]State{
		WarmFree: nil, Resetting: {Resetting}, Leased: {Resetting, Leased},
		Quarantined: {Quarantined}, Retired: {Retired},
	}
	for _, to := range steps[want] {
		if err := h.Transition(to, nil); err != nil {
			t.Fatalf("drive %s: %v", to, err)
		}
	}
	return h
}

func TestEveryTransitionAppliesOrIsRejected(t *testing.T) {
	for _, from := range allStates {
		for _, to := range allStates {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				s, _ := newTestStore(t, "a")
				h := drive(t, s, "a", from)
				defer h.Unlock()
				err := h.Transition(to, nil)
				if CanTransition(from, to) {
					if err != nil || h.Record().State != to {
						t.Fatalf("err=%v state=%s", err, h.Record().State)
					}
					disk, _ := s.ReadRecord("a")
					if disk.State != to {
						t.Fatalf("disk state %s", disk.State)
					}
					return
				}
				var te *TransitionError
				if !errors.As(err, &te) || te.From != from || te.To != to {
					t.Fatalf("want TransitionError, got %v", err)
				}
				if disk, _ := s.ReadRecord("a"); disk.State != from {
					t.Fatalf("rejected move changed disk to %s", disk.State)
				}
			})
		}
	}
}

func TestLeaseLifecycleWithFakeClock(t *testing.T) {
	s, clk := newTestStore(t, "a")
	start := clk.Now()
	h, err := s.Acquire(AcquireRequest{OwnerSessionID: "sess-1", BeadHint: "bead-1", Repo: "r", Commit: "c1", Branch: "lease/x", TTL: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	r := h.Record()
	if r.State != Resetting || r.LeaseID != "lease-1" || r.OwnerSessionID != "sess-1" || r.BeadHint != "bead-1" ||
		r.Repo != "r" || r.Commit != "c1" || r.Branch != "lease/x" ||
		!r.Acquired.Equal(start) || !r.Heartbeat.Equal(start) || !r.Expiry.Equal(start.Add(10*time.Minute)) {
		t.Fatalf("record: %+v", r)
	}
	if err := h.Renew(time.Minute); !errors.Is(err, ErrNotLeased) {
		t.Fatalf("renew while resetting: %v", err)
	}
	if err := h.MarkLeased(); err != nil {
		t.Fatal(err)
	}
	clk.Advance(4 * time.Minute)
	if err := h.Renew(10 * time.Minute); err != nil {
		t.Fatal(err)
	}
	r = h.Record()
	if !r.Heartbeat.Equal(start.Add(4*time.Minute)) || !r.Expiry.Equal(start.Add(14*time.Minute)) || !r.Acquired.Equal(start) {
		t.Fatalf("after renew: %+v", r)
	}
	if err := h.BeginReset(); err != nil {
		t.Fatal(err)
	}
	if err := h.FinishReset(); err != nil {
		t.Fatal(err)
	}
	r = h.Record()
	if r.State != WarmFree || r.LeaseID != "" || r.OwnerSessionID != "" || !r.Expiry.IsZero() || r.BeadHint != "" || r.LastBeadHint != "bead-1" {
		t.Fatalf("after release: %+v", r)
	}
	h.Unlock()
	if held, _ := s.Held("a"); held {
		t.Fatal("still held after Unlock")
	}
}

func TestQuarantineRetireAndUnquarantine(t *testing.T) {
	s, _ := newTestStore(t, "a", "b")
	h, _ := s.Acquire(AcquireRequest{OwnerSessionID: "s", TTL: time.Hour})
	_ = h.MarkLeased()
	if err := h.Quarantine("reset failed"); err != nil {
		t.Fatal(err)
	}
	if r := h.Record(); r.State != Quarantined || r.Reason != "reset failed" || r.OwnerSessionID != "s" {
		t.Fatalf("quarantine keeps evidence: %+v", r)
	}
	h.Unlock()
	// Quarantined slots are never handed out.
	h2, err := s.Acquire(AcquireRequest{OwnerSessionID: "t", TTL: time.Hour})
	if err != nil || h2.Record().Slot != "b" {
		t.Fatalf("expected slot b, got %v %v", err, h2)
	}
	h2.Unlock()
	q, _ := s.Lock("a")
	if err := q.BeginReset(); err != nil { // operator un-quarantine
		t.Fatal(err)
	}
	_ = q.FinishReset()
	if err := q.Retire("done"); err != nil {
		t.Fatal(err)
	}
	if r := q.Record(); r.State != Retired || r.Reason != "done" || r.OwnerSessionID != "" {
		t.Fatalf("retired: %+v", r)
	}
	q.Unlock()
}

func TestOwnerIsSessionIDAndBeadIsOnlyAHint(t *testing.T) {
	s, _ := newTestStore(t, "a", "b", "c")
	if _, err := s.Acquire(AcquireRequest{BeadHint: "bead-1", TTL: time.Hour}); !errors.Is(err, ErrOwner) {
		t.Fatalf("empty owner: %v", err)
	}
	h1, err := s.Acquire(AcquireRequest{OwnerSessionID: "sess-1", BeadHint: "bead-1", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.Acquire(AcquireRequest{OwnerSessionID: "sess-2", BeadHint: "bead-1", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Unlock()
	defer h2.Unlock()
	if h1.Record().Slot == h2.Record().Slot {
		t.Fatal("two sessions on one bead must hold two slots")
	}
	// Lookup is by session id; a bead id is not a key.
	if r, ok, _ := s.FindByOwner("sess-2"); !ok || r.OwnerSessionID != "sess-2" {
		t.Fatalf("FindByOwner: %v %+v", ok, r)
	}
	if _, ok, _ := s.FindByOwner("bead-1"); ok {
		t.Fatal("bead id must never resolve a lease")
	}
	// On disk the owner field is the session id; the bead is a separate hint.
	raw, _ := os.ReadFile(filepath.Join(s.slotPath(h1.Record().Slot), recordName))
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["owner_session_id"] != "sess-1" || m["bead_hint"] != "bead-1" {
		t.Fatalf("json: %s", raw)
	}
	for k := range m {
		if strings.Contains(k, "key") {
			t.Errorf("unexpected key field %q", k)
		}
	}
}

func TestAffinityPrefersLastSlotButNeverBlocksOthers(t *testing.T) {
	s, _ := newTestStore(t, "a", "b", "c")
	h, _ := s.Acquire(AcquireRequest{OwnerSessionID: "s1", BeadHint: "bead-9", TTL: time.Hour})
	if h.Record().Slot != "a" {
		t.Fatalf("first free slot expected, got %s", h.Record().Slot)
	}
	_ = h.FinishReset()
	h.Unlock()
	// Move bead-9's memory onto slot c by leasing it there.
	hc, _ := s.Lock("c")
	_ = hc.Transition(Resetting, func(r *Record) { r.LastBeadHint = "bead-9" })
	_ = hc.FinishReset()
	hc.Unlock()
	h2, err := s.Acquire(AcquireRequest{OwnerSessionID: "s2", BeadHint: "bead-9", TTL: time.Hour})
	if err != nil || h2.Record().Slot != "a" && h2.Record().Slot != "c" {
		t.Fatalf("affinity: %v", err)
	}
	// Both a and c remember bead-9; a sorts first among preferred.
	if h2.Record().Slot != "a" {
		t.Fatalf("got %s", h2.Record().Slot)
	}
	// Preferred slot is leased: another slot is used, affinity never blocks.
	h3, err := s.Acquire(AcquireRequest{OwnerSessionID: "s3", BeadHint: "bead-9", TTL: time.Hour})
	if err != nil || h3.Record().Slot != "c" {
		t.Fatalf("expected c, got %v %v", err, h3)
	}
	h2.Unlock()
	h3.Unlock()
}

// BeginResetFromResetting finishes a reset that Acquire left in Resetting.
func (h *Handle) BeginResetFromResetting() error { return h.FinishReset() }

func TestExhaustedAndBusySlotsSkipped(t *testing.T) {
	s, _ := newTestStore(t, "a")
	h, err := s.Acquire(AcquireRequest{OwnerSessionID: "s", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(AcquireRequest{OwnerSessionID: "t", TTL: time.Hour}); !errors.Is(err, ErrExhausted) {
		t.Fatalf("want exhausted, got %v", err)
	}
	if _, err := s.Lock("a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("want busy, got %v", err)
	}
	if _, err := s.Lock("nope"); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("want no slot, got %v", err)
	}
	h.Unlock()
	h.Unlock() // idempotent
	if err := h.Transition(Leased, nil); err == nil {
		t.Fatal("transition on unlocked handle must fail")
	}
}

func TestCreateSlotValidation(t *testing.T) {
	s, _ := newTestStore(t, "a")
	if err := s.CreateSlot("a"); !errors.Is(err, ErrExists) {
		t.Fatalf("dup: %v", err)
	}
	for _, bad := range []string{"", ".", "..", "x/y"} {
		if err := s.CreateSlot(bad); err == nil {
			t.Errorf("slot id %q accepted", bad)
		}
	}
}

func TestLeakDetectionUsesLockNotRecord(t *testing.T) {
	s, clk := newTestStore(t, "a")
	h, _ := s.Acquire(AcquireRequest{OwnerSessionID: "s", TTL: 5 * time.Minute})
	_ = h.MarkLeased()

	st, _ := s.Inspect("a")
	if !st.Held || st.Leak {
		t.Fatalf("live holder: %+v", st)
	}
	// Expiry passed but the holder still holds the flock: not a leak.
	clk.Advance(time.Hour)
	if st, _ = s.Inspect("a"); !st.Held || st.Leak {
		t.Fatalf("expired but held must not be a leak: %+v", st)
	}
	// Holder gone (fd closed), expiry passed: leak.
	h.Unlock()
	if st, _ = s.Inspect("a"); st.Held || !st.Leak || st.Record.State != Leased {
		t.Fatalf("dead holder past expiry: %+v", st)
	}
	// Holder gone but expiry not passed: not yet a leak.
	h2, _ := s.Lock("a")
	_ = h2.Renew(time.Hour)
	h2.Unlock()
	if st, _ = s.Inspect("a"); st.Held || st.Leak {
		t.Fatalf("unexpired: %+v", st)
	}
}

// Real process death: a child holds the slot's flock; killing it frees the lock.
func TestFlockReleasedWhenHolderProcessDies(t *testing.T) {
	s, _ := newTestStore(t, "a")
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperHoldFlock")
	cmd.Env = append(os.Environ(), "POOL_HELPER_LOCK="+filepath.Join(s.slotPath("a"), lockName))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	if _, err := stdout.Read(buf); err != nil || string(buf) != "locked" {
		_ = cmd.Process.Kill()
		t.Fatalf("helper not ready: %q %v", buf, err)
	}
	if held, _ := s.Held("a"); !held {
		t.Fatal("child holds the lock; Held must be true")
	}
	if _, err := s.Lock("a"); !errors.Is(err, ErrBusy) {
		t.Fatalf("want busy, got %v", err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if held, _ := s.Held("a"); held {
		t.Fatal("lock must be free after the holder died")
	}
}

func TestHelperHoldFlock(t *testing.T) {
	path := os.Getenv("POOL_HELPER_LOCK")
	if path == "" {
		t.Skip("helper process only")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		os.Exit(3)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		os.Exit(4)
	}
	fmt.Print("locked")
	time.Sleep(time.Minute) // killed by the parent
	os.Exit(0)
}

func TestAtomicWriteFailureKeepsPreviousRecord(t *testing.T) {
	s, _ := newTestStore(t, "a")
	h, _ := s.Acquire(AcquireRequest{OwnerSessionID: "s", TTL: time.Hour})
	before, _ := os.ReadFile(filepath.Join(s.slotPath("a"), recordName))
	s.rename = func(_, _ string) error { return errors.New("injected rename failure") }
	if err := h.MarkLeased(); err == nil {
		t.Fatal("expected failure")
	}
	if h.Record().State != Resetting {
		t.Fatalf("in-memory state advanced despite failed write: %s", h.Record().State)
	}
	after, _ := os.ReadFile(filepath.Join(s.slotPath("a"), recordName))
	if string(before) != string(after) {
		t.Fatalf("record changed:\n%s\n%s", before, after)
	}
	assertNoTemp(t, s.slotPath("a"))
	h.Unlock()
}

func TestAtomicWriteLeavesNoTempAndReadersNeverSeeTornData(t *testing.T) {
	s, _ := newTestStore(t, "a")
	h, _ := s.Acquire(AcquireRequest{OwnerSessionID: "s", TTL: time.Hour})
	_ = h.MarkLeased()
	for i := 0; i < 20; i++ {
		if err := h.Renew(time.Hour); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadRecord("a"); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	assertNoTemp(t, s.slotPath("a"))
	h.Unlock()
}

func TestStaleTempFileIsIgnoredAndCorruptRecordReported(t *testing.T) {
	s, _ := newTestStore(t, "a")
	dir := s.slotPath("a")
	if err := os.WriteFile(filepath.Join(dir, recordName+".tmp-crash"), []byte("{torn"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, err := s.ReadRecord("a"); err != nil || r.State != WarmFree {
		t.Fatalf("stale temp must not matter: %v %+v", err, r)
	}
	if err := os.WriteFile(filepath.Join(dir, recordName), []byte("{torn"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadRecord("a"); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("want corrupt error, got %v", err)
	}
	if _, err := s.ReadRecord("missing"); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("want ErrNoSlot, got %v", err)
	}
}

func TestStatusEmptyAndSlotsOrder(t *testing.T) {
	s := NewStore(t.TempDir(), nil)
	if sts, err := s.Status(); err != nil || len(sts) != 0 {
		t.Fatalf("empty: %v %v", sts, err)
	}
	for _, id := range []string{"b", "a"} {
		_ = s.CreateSlot(id)
	}
	ids, _ := s.Slots()
	if strings.Join(ids, ",") != "a,b" {
		t.Fatalf("order: %v", ids)
	}
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

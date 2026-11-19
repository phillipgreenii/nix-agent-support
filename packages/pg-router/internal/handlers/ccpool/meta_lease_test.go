package ccpool

import (
	"testing"
	"time"
)

// The first-owner lease keys of a fresh session: owner, generation 1, one supervisor.
func TestStampLeaseOwner_stampsFirstOwnerKeys(t *testing.T) {
	m := map[string]string{}
	StampLeaseOwner(m, "owner-1")
	if m[MetaKeyLeaseOwner] != "owner-1" || m[MetaKeyLeaseGeneration] != "1" || m[MetaKeySupervisors] != "1" {
		t.Errorf("StampLeaseOwner = %v", m)
	}
	empty := map[string]string{}
	StampLeaseOwner(empty, "")
	if len(empty) != 0 {
		t.Errorf("an empty owner must stamp nothing, got %v", empty)
	}
}

func TestLeaseGeneration_readsAbsentBadAndNegativeAsZero(t *testing.T) {
	for name, meta := range map[string]map[string]string{
		"absent":   {},
		"garbage":  {MetaKeyLeaseGeneration: "x"},
		"negative": {MetaKeyLeaseGeneration: "-3"},
	} {
		if got := ParseLeaseGeneration(meta); got != 0 {
			t.Errorf("%s: ParseLeaseGeneration = %d, want 0", name, got)
		}
	}
	s := Session{Meta: map[string]string{MetaKeyLeaseGeneration: "7"}}
	if got := s.LeaseGeneration(); got != 7 {
		t.Errorf("LeaseGeneration = %d, want 7", got)
	}
}

func TestLeaseSupervisors_defaultsToOne(t *testing.T) {
	if got := (Session{}).LeaseSupervisors(); got != 1 {
		t.Errorf("absent supervisors = %d, want 1", got)
	}
	if got := (Session{Meta: map[string]string{MetaKeySupervisors: "0"}}).LeaseSupervisors(); got != 1 {
		t.Errorf("zero supervisors = %d, want 1", got)
	}
	if got := (Session{Meta: map[string]string{MetaKeySupervisors: "3"}}).LeaseSupervisors(); got != 3 {
		t.Errorf("supervisors = %d, want 3", got)
	}
}

func TestParseReminderSent_readsTheLatch(t *testing.T) {
	at := time.Date(2026, 11, 1, 3, 4, 5, 0, time.UTC)
	got, ok := ParseReminderSent(map[string]string{MetaKeyReminderSent: FormatMetaTime(at)})
	if !ok || !got.Equal(at) {
		t.Errorf("ParseReminderSent = %v, %v", got, ok)
	}
	if _, ok := ParseReminderSent(map[string]string{}); ok {
		t.Error("an absent latch must read as not sent")
	}
}

func TestKeepsWorktree_followsTheMarker(t *testing.T) {
	if (Session{}).KeepsWorktree() {
		t.Error("no marker must not keep the worktree")
	}
	if !(Session{Meta: map[string]string{MetaKeyKeepWorktree: "1"}}).KeepsWorktree() {
		t.Error("the marker must keep the worktree")
	}
}

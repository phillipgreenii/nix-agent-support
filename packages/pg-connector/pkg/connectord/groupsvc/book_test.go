package groupsvc

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

func TestClassForIsClass3ForAMemberAndClass4Otherwise(t *testing.T) {
	if got := classFor(true); got != governor.ClassQuery {
		t.Errorf("classFor(true) = %v, want class 3", got)
	}
	if got := classFor(false); got != governor.ClassKeepAlive {
		t.Errorf("classFor(false) = %v, want class 4", got)
	}
}

func TestPurposeOfFollowsTheClass(t *testing.T) {
	tests := []struct {
		class governor.Class
		want  governor.Purpose
	}{
		{governor.ClassInteractive, governor.PurposeInteractive},
		{governor.ClassInvalidated, governor.PurposeRefresh},
		{governor.ClassQuery, governor.PurposeRefresh},
		{governor.ClassKeepAlive, governor.PurposeRefresh},
		{governor.ClassFill, governor.PurposeFill},
	}
	for _, tc := range tests {
		if got := purposeOf(tc.class); got != tc.want {
			t.Errorf("purposeOf(%v) = %v, want %v", tc.class, got, tc.want)
		}
	}
}

func TestDueOfFollowsTheRulesTable(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	until := t0.Add(7 * time.Minute)
	tests := []struct {
		name    string
		group   string
		row     store.GroupRow
		exists  bool
		wantDue time.Time
		wantOK  bool
	}{
		{"a group outside the background cycle is never booked", groupPassive, store.GroupRow{FreshUntil: until}, true, time.Time{}, false},
		{"a stored row is due at its fresh_until", groupSide, store.GroupRow{FreshUntil: until}, true, until, true},
		{"a stamped row with no fresh_until has no time-based task", groupStamped, store.GroupRow{}, true, time.Time{}, false},
		{"a stamped row with a fresh_until is due at it", groupStamped, store.GroupRow{FreshUntil: until}, true, until, true},
		{"a missing row of a timed group is due now", groupSide, store.GroupRow{}, false, time.Time{}, true},
		{"an untimed row of a timed group is due now", groupSide, store.GroupRow{}, true, time.Time{}, true},
		{"a missing row of a group with no TTL has no task", groupStamped, store.GroupRow{}, false, time.Time{}, false},
		{"a group the table does not list has no task", "unlisted", store.GroupRow{}, false, time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			due, ok := r.svc.dueOf(tc.group, tc.row, tc.exists)
			if ok != tc.wantOK || !due.Equal(tc.wantDue) {
				t.Errorf("dueOf = %v, %v, want %v, %v", due, ok, tc.wantDue, tc.wantOK)
			}
		})
	}
}

func TestBackgroundTaskBooksOnlyEntitiesInTheKeepAliveSet(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := t.Context()
	r.seedPrimary("ent-member", "p")
	r.seedPrimary("ent-kept", "p")
	r.seedPrimary("ent-idle", "p")
	r.keep.keep("ent-kept")
	members := map[string]bool{"ent-member": true}

	tests := []struct {
		id        string
		wantOK    bool
		wantClass governor.Class
	}{
		{"ent-member", true, governor.ClassQuery},
		{"ent-kept", true, governor.ClassKeepAlive},
		{"ent-idle", false, 0},
		{"ent-unseen", false, 0},
	}
	for _, tc := range tests {
		task, ok, err := r.svc.backgroundTask(ctx, tc.id, groupSide, store.GroupRow{}, false, members)
		if err != nil || ok != tc.wantOK {
			t.Fatalf("%s: ok = %v, err = %v, want ok %v", tc.id, ok, err, tc.wantOK)
		}
		if ok && (task.Class != tc.wantClass || task.Kind != groupSide || task.Group != groupSide || task.Entity != tc.id || task.Access.IsZero()) {
			t.Errorf("%s: task = %+v, want class %v of group %s with the entity's access", tc.id, task, tc.wantClass, groupSide)
		}
	}
}

func TestABookingOfATombstonedEntityIsDropped(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := t.Context()
	r.seedPrimary(ent1, "p")
	ent, _, err := r.st.GetEntity(ctx, r.kind(), ent1)
	if err != nil {
		t.Fatal(err)
	}
	ent.TombstonedAt = r.clk.Now()
	if err := r.st.PutEntity(ctx, ent); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := r.svc.backgroundTask(ctx, ent1, groupSide, store.GroupRow{}, false, map[string]bool{ent1: true}); err != nil || ok {
		t.Errorf("a tombstoned member was booked: ok %v, err %v", ok, err)
	}
}

func TestBookAfterReplacesABackgroundTaskButLeavesAQueuedClass2TaskAlone(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := t.Context()
	r.seedPrimary(ent1, "p")
	r.keep.keep(ent1)
	row := freshGroup(r, ent1, groupSide, "d")
	r.seedGroup(row)

	// Nothing queued: the next cycle is booked, class 4, at the stored fresh_until.
	if err := r.svc.bookAfter(ctx, ent1, groupSide, nil); err != nil {
		t.Fatal(err)
	}
	if c, queued, _, _ := r.svc.Inspect(ctx, ent1, groupSide); !queued || c != governor.ClassKeepAlive {
		t.Fatalf("after bookAfter: class %v queued %v, want a class 4 task", c, queued)
	}
	// A later booking replaces it in full (here: as a member, class 3).
	if err := r.svc.bookAfter(ctx, ent1, groupSide, map[string]bool{ent1: true}); err != nil {
		t.Fatal(err)
	}
	if c, _, _, _ := r.svc.Inspect(ctx, ent1, groupSide); c != governor.ClassQuery {
		t.Errorf("class = %v, want the class 3 replacement", c)
	}
	// A queued class 2 task is protected: bookAfter does not replace it.
	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.bookAfter(ctx, ent1, groupSide, nil); err != nil {
		t.Fatal(err)
	}
	if c, _, _, _ := r.svc.Inspect(ctx, ent1, groupSide); c != governor.ClassInvalidated {
		t.Errorf("class = %v, want the class 2 task kept", c)
	}
}

func TestBookReadOnlyEverMakesABookingMoreUrgent(t *testing.T) {
	r := newRig(t, specAlpha, nil, nil)
	ctx := t.Context()
	r.seedPrimary(ent1, "p")
	r.keep.keep(ent1)
	row := freshGroup(r, ent1, groupSide, "d")
	r.seedGroup(row)

	if err := r.svc.QueueGroup(ctx, ent1, groupSide, governor.ClassInvalidated, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// A read of the stored row books class 4: it merges with the queued class 2
	// task and does not replace it.
	if err := r.svc.bookRead(ctx, ent1, groupSide, row, nil); err != nil {
		t.Fatal(err)
	}
	if c, queued, _, _ := r.svc.Inspect(ctx, ent1, groupSide); !queued || c != governor.ClassInvalidated {
		t.Errorf("class %v queued %v, want the class 2 task kept", c, queued)
	}
	// A group outside the background cycle books nothing.
	r.seedGroup(freshGroup(r, ent1, groupPassive, "x"))
	if err := r.svc.bookRead(ctx, ent1, groupPassive, freshGroup(r, ent1, groupPassive, "x"), nil); err != nil {
		t.Fatal(err)
	}
	if _, queued, _, _ := r.svc.Inspect(ctx, ent1, groupPassive); queued {
		t.Error("a passive group was booked")
	}
}

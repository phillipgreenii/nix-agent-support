package obs

import (
	"errors"
	"fmt"
	"syscall"
	"testing"
)

func TestNoteNoSpaceCountsAndLogsOnlyAFullVolume(t *testing.T) {
	o := New(nil)
	o.RegisterStandard()
	for _, op := range NoSpaceOps {
		if v, ok := o.Reg.Counter(MBlockedNoSpace, map[string]string{"op": op}); !ok || v != 0 {
			t.Fatalf("op %s not registered at zero: %v %v", op, v, ok)
		}
	}
	full := fmt.Errorf("write seg: %w", syscall.ENOSPC)
	if !o.NoteNoSpace(OpArchive, full, "100", "7") {
		t.Fatal("ENOSPC not recognised")
	}
	if v, _ := o.Reg.Counter(MBlockedNoSpace, map[string]string{"op": OpArchive}); v != 1 {
		t.Fatalf("archive count %v, want 1", v)
	}
	evs := o.EventsNamed(EvBlockedNoSpace)
	if len(evs) != 1 || evs[0].Attrs["op"] != OpArchive || evs[0].Attrs["needed_bytes"] != "100" || evs[0].Attrs["had_bytes"] != "7" {
		t.Fatalf("events %v", evs)
	}
	if !o.NoteNoSpace(OpParseIndex, errors.New("database or disk is full"), "", "") {
		t.Fatal("sqlite full not recognised")
	}
	if e := o.EventsNamed(EvBlockedNoSpace); e[1].Attrs["needed_bytes"] != "unknown" {
		t.Fatalf("unknown figure: %v", e[1].Attrs)
	}
	before := len(o.EventsNamed(EvBlockedNoSpace))
	if o.NoteNoSpace(OpCopy, errors.New("permission denied"), "", "") || o.NoteNoSpace(OpCopy, nil, "", "") {
		t.Fatal("a non-space error was counted")
	}
	if len(o.EventsNamed(EvBlockedNoSpace)) != before {
		t.Fatal("event for a non-space error")
	}
}

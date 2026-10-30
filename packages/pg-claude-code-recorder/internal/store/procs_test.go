package store

import (
	"errors"
	"testing"
)

func TestProcObservationsAreIdempotentAndSurviveARebuild(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	d := ProcRec("discover", 7, "Fri Oct 30 15:50:54 2026", 100)
	d.Val, d.Reason = `{"kind":"other"}`, "stale"
	x := ProcRec("exit", 7, "Fri Oct 30 15:50:54 2026", 300)
	x.Reason, x.Val = ExitObserved, "present"
	if err := s.Commit(d, ProcRec("seen", 7, "Fri Oct 30 15:50:54 2026", 200), ProcRec("seen", 7, "Fri Oct 30 15:50:54 2026", 150), x); err != nil {
		t.Fatal(err)
	}
	// A second exit record changes nothing: the first one wins.
	late := ProcRec("exit", 7, "Fri Oct 30 15:50:54 2026", 999)
	late.Reason, late.Val = ExitUnobserved, "absent"
	if err := s.Commit(late); err != nil {
		t.Fatal(err)
	}
	check := func(s *Store) {
		t.Helper()
		p, err := s.Proc(7, "Fri Oct 30 15:50:54 2026")
		if err != nil {
			t.Fatal(err)
		}
		if p.FirstSeenNS != 100 || p.LastSeenNS != 200 || p.ExitNS != 300 || p.ExitKind != ExitObserved || !p.RegistryPresent || !p.Stale || p.Host != `{"kind":"other"}` {
			t.Fatalf("proc %+v", p)
		}
		if _, err := s.Proc(7, "another start"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a reused pid with another start time: %v", err)
		}
	}
	check(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir, Options{Rebuild: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	check(s2)
	rows, err := s2.Procs()
	if err != nil || len(rows) != 1 {
		t.Fatalf("procs %v %v", rows, err)
	}
}

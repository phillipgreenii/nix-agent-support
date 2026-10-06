package store

import "testing"

func TestListMetaPrefixMatchesLiterally(t *testing.T) {
	s := OpenForTest(t)
	for k, v := range map[string]string{
		"source_fetch.a.mine": "1",
		"source_fetch.b.team": "2",
		"source_fetchXa":      "no: prefix has a literal dot",
		"last_heartbeat":      "no",
		"100%_done":           "wildcard-looking key",
	} {
		if err := s.SetMeta(k, v); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListMetaPrefix("source_fetch.")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["source_fetch.a.mine"] != "1" || got["source_fetch.b.team"] != "2" {
		t.Errorf("ListMetaPrefix(source_fetch.) = %v, want exactly the two source_fetch. keys", got)
	}

	// A LIKE-style wildcard in the prefix MUST NOT widen the match.
	got, err = s.ListMetaPrefix("%")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("ListMetaPrefix(%%) = %v, want none", got)
	}
	got, err = s.ListMetaPrefix("100%_")
	if err != nil || len(got) != 1 {
		t.Errorf("ListMetaPrefix(100%%_) = %v, %v; want the one literal match", got, err)
	}

	got, err = s.ListMetaPrefix("nothing.")
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("ListMetaPrefix(nothing.) = %v, %v; want a non-nil empty map", got, err)
	}
}

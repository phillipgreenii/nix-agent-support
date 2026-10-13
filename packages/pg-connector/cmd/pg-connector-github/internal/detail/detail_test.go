package detail

import (
	"testing"
)

func TestDecode_RoundTripsTheStoredContent(t *testing.T) {
	d := Detail{Merged: true, MergeStateStatus: "CLEAN", ReviewRequests: []string{"b", "a"}, Additions: 5, Deletions: 2, ChangedFiles: 3}
	content, err := d.canonical()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(content)
	if err != nil {
		t.Fatal(err)
	}
	if got.Merged != d.Merged || got.MergeStateStatus != d.MergeStateStatus || got.Additions != 5 || got.Deletions != 2 ||
		got.ChangedFiles != 3 || len(got.ReviewRequests) != 2 || got.ReviewRequests[0] != "b" {
		t.Errorf("Decode = %+v, want %+v", got, d)
	}
}

func TestDecode_RejectsNoContentAndGarbage(t *testing.T) {
	for name, in := range map[string][]byte{"nil": nil, "empty": {}, "garbage": []byte("nope")} {
		if _, err := Decode(in); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestCanonical_NoReviewRequestsIsAnEmptyArray(t *testing.T) {
	b, err := Detail{}.canonical()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"merged":false,"mergeStateStatus":"","reviewRequests":[],"additions":0,"deletions":0,"changedFiles":0}`
	if string(b) != want {
		t.Errorf("canonical = %s, want %s", b, want)
	}
}

func TestFingerprint(t *testing.T) {
	base := Detail{MergeStateStatus: "BLOCKED", ReviewRequests: []string{"alice", "bob", "widget-reviewers"}, Additions: 1}
	reordered := base
	reordered.ReviewRequests = []string{"widget-reviewers", "alice", "bob"}
	if Fingerprint(base) != Fingerprint(reordered) {
		t.Error("a reordered reviewRequests answer changed the fingerprint")
	}
	if base.ReviewRequests[0] != "alice" || reordered.ReviewRequests[0] != "widget-reviewers" {
		t.Error("Fingerprint sorted the caller's slice")
	}
	for name, mutate := range map[string]func(*Detail){
		"mergeStateStatus": func(d *Detail) { d.MergeStateStatus = "CLEAN" },
		"merged":           func(d *Detail) { d.Merged = true },
		"reviewer added":   func(d *Detail) { d.ReviewRequests = append(slicesClone(d.ReviewRequests), "carol") },
		"additions":        func(d *Detail) { d.Additions++ },
		"deletions":        func(d *Detail) { d.Deletions++ },
		"changedFiles":     func(d *Detail) { d.ChangedFiles++ },
	} {
		c := base
		mutate(&c)
		if Fingerprint(c) == Fingerprint(base) {
			t.Errorf("%s: fingerprint did not change", name)
		}
	}
	if Fingerprint(Detail{}) != Fingerprint(Detail{ReviewRequests: []string{}}) {
		t.Error("nil and empty reviewRequests differ")
	}
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

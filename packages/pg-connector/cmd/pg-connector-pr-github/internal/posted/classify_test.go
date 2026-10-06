package posted

import (
	"reflect"
	"testing"
)

// The situation table: each row of the design's idempotence table.
func TestClassify_SituationTable(t *testing.T) {
	fp := PointFingerprint("a.go", "", 1, "x")
	cases := []struct {
		name     string
		onGitHub map[string]bool
		sidecar  State
		want     Verdict
	}{
		{"exact replay: marker found on GitHub", map[string]bool{fp: true}, State{Fingerprints: []string{fp}}, AlreadyPresent},
		{"operator deleted a posted comment: in sidecar, nowhere on GitHub", map[string]bool{}, State{Fingerprints: []string{fp}}, Dismissed},
		{"operator submitted the review: marker in submitted review", map[string]bool{fp: true}, State{Fingerprints: []string{fp}}, AlreadyPresent},
		{"state dir wiped, comment still on GitHub", map[string]bool{fp: true}, State{}, AlreadyPresent},
		{"state dir wiped, comment deleted earlier: re-posted", map[string]bool{}, State{}, ToWrite},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify([]string{fp}, tc.onGitHub, tc.sidecar)
			if !reflect.DeepEqual(got, []Verdict{tc.want}) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestClassify_MixedRequestKeepsOrder(t *testing.T) {
	present, gone, fresh := "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "cccccccccccccccc"
	got := Classify(
		[]string{fresh, present, gone},
		map[string]bool{present: true},
		State{Fingerprints: []string{gone, present}},
	)
	want := []Verdict{ToWrite, AlreadyPresent, Dismissed}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestClassify_EmptyAndNilGitHub(t *testing.T) {
	if got := Classify(nil, nil, State{}); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if got := Classify([]string{"x"}, nil, State{}); !reflect.DeepEqual(got, []Verdict{ToWrite}) {
		t.Fatalf("got %v", got)
	}
}

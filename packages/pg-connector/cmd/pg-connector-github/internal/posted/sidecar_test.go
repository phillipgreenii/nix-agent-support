package posted

import (
	"reflect"
	"testing"
)

func TestState_HasAndAddFingerprintsSkipDuplicates(t *testing.T) {
	var s State
	if s.Has("aaaaaaaaaaaaaaaa") {
		t.Fatal("an empty state has nothing")
	}
	s.AddFingerprints("aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "aaaaaaaaaaaaaaaa")
	if want := []string{"aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"}; !reflect.DeepEqual(s.Fingerprints, want) {
		t.Fatalf("fingerprints = %v, want %v", s.Fingerprints, want)
	}
	if !s.Has("bbbbbbbbbbbbbbbb") || s.Has("cccccccccccccccc") {
		t.Fatal("Has disagrees with the recorded fingerprints")
	}
}

func TestState_AddBodyHeadSkipsDuplicates(t *testing.T) {
	var s State
	s.AddBodyHead("abc")
	s.AddBodyHead("def")
	s.AddBodyHead("abc")
	if want := []string{"abc", "def"}; !reflect.DeepEqual(s.BodyHeads, want) {
		t.Fatalf("body heads = %v, want %v", s.BodyHeads, want)
	}
}

// Package vocab holds the invented names of the shared connector-daemon tests
// and the switch that runs a whole test set over a second registration made of
// different invented names.
//
// The tests of engine, query, feed and feedop are in-package tests, so they
// cannot import enginetest (which imports those packages). Both can import
// this package, which imports nothing of the library.
//
// Every test file declares its registration vocabulary with W, the word of the
// first registration:
//
//	var kindWidget = vocab.W("widget")
//
// W returns that word unchanged in a normal run and the word of the second
// registration when the environment variable named by Env is set to Second. A
// word the table lacks panics, so a name that was never routed through W cannot
// hide: the second registration would then still contain the first one's
// literal and the test that compares against it would fail.
//
// RerunUnderSecond runs the current test binary again, every test but itself,
// with the second registration selected; one test of each package calls it, so
// "go test" of the package runs its whole set over both registrations.
package vocab

import (
	"os"
	"os/exec"
	"testing"
)

const (
	// Env is the environment variable that selects the registration.
	Env = "PG_ENGINETEST_REGISTRATION"
	// Second is the value of Env that selects the second registration.
	Second = "second"
)

// secondWords maps each word of the first registration to the word the second
// one uses in its place. Every second word is a different invented lower_snake
// word; none is a key of this table, and none is one of the fixed words of the
// wire protocol.
var secondWords = map[string]string{
	// Entity kinds, views and groups.
	"widget":         "gizmo",
	"example_entity": "sample_thing",
	"all":            "everything",
	"flagged":        "marked",
	"main":           "core",
	"alpha":          "omega",
	"beta":           "psi",
	"gamma":          "chi",
	"delta":          "rho",
	// Change kinds and the four roles.
	"retitled":        "renamed",
	"toggled":         "flipped",
	"tweaked":         "nudged",
	"gone":            "vanished",
	"joined":          "boarded",
	"left":            "departed",
	"added":           "arrived",
	"removed":         "erased",
	"first":           "debut",
	"enter_set":       "board_set",
	"leave_set":       "depart_set",
	"entered_scope":   "boarded_scope",
	"left_scope":      "departed_scope",
	"content_changed": "body_altered",
	"flag_changed":    "marker_flipped",
	"other_changed":   "misc_altered",
	// The word only the invented fake connector of enginetest adds.
	"extra": "side",
}

// IsSecond reports whether the second registration is selected.
func IsSecond() bool { return os.Getenv(Env) == Second }

// W returns the word of the running registration that stands for w, a word of
// the first registration. It panics for a word the table does not hold while
// the second registration is selected.
func W(w string) string {
	if !IsSecond() {
		return w
	}
	s, ok := secondWords[w]
	if !ok {
		panic("vocab: no second-registration word for " + w)
	}
	return s
}

// Words returns a copy of the table: each first-registration word and its
// second-registration word.
func Words() map[string]string {
	out := make(map[string]string, len(secondWords))
	for k, v := range secondWords {
		out[k] = v
	}
	return out
}

// RerunUnderSecond runs the test binary of the calling test again with the
// second registration selected and every test but the caller selected, and
// fails the caller with the child's output when the child fails. Under the
// second registration it skips itself, so the re-run does not recurse.
func RerunUnderSecond(t *testing.T) {
	t.Helper()
	if IsSecond() {
		t.Skip("already running over the second registration")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=.", "-test.count=1", "-test.skip=^"+t.Name()+"$")
	cmd.Env = append(os.Environ(), Env+"="+Second)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the test set failed over the second registration: %v\n%s", err, out)
	}
}

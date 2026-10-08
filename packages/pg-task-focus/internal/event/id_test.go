package event_test

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func TestNewIDMonotonicPrefix(t *testing.T) {
	entropy := rand.New(rand.NewSource(7))
	base := time.Date(2026, 10, 7, 13, 30, 0, 0, time.UTC)

	var prev string
	for i := 0; i < 50; i++ {
		id := string(event.NewID(base.Add(time.Duration(i)*time.Millisecond*37), entropy))
		if len(id) != 26 {
			t.Fatalf("len(%q) = %d, want 26", id, len(id))
		}
		for _, r := range id {
			if !strings.ContainsRune(crockford, r) {
				t.Fatalf("%q holds %q, which is not in the Crockford alphabet", id, r)
			}
		}
		if prev != "" && id[:10] <= prev[:10] {
			t.Fatalf("time prefix %q does not sort after %q", id[:10], prev[:10])
		}
		prev = id
	}
}

func TestNewIDIsDeterministicForAFixedClockAndEntropy(t *testing.T) {
	at := time.Date(2026, 10, 7, 13, 30, 0, 0, time.UTC)
	a := event.NewID(at, rand.New(rand.NewSource(1)))
	b := event.NewID(at, rand.New(rand.NewSource(1)))
	c := event.NewID(at, rand.New(rand.NewSource(2)))
	if a != b {
		t.Errorf("same time and entropy gave %q and %q", a, b)
	}
	if a == c {
		t.Errorf("different entropy gave the same id %q", a)
	}
	if a[:10] != c[:10] {
		t.Errorf("same time gave prefixes %q and %q", a[:10], c[:10])
	}
}

func TestNewIDEncodesTheMillisecondInTheFirstTenCharacters(t *testing.T) {
	// 2026-10-07T13:30:00.000Z is 1791379800000 ms since the epoch.
	id := event.NewID(time.UnixMilli(1791379800000), rand.New(rand.NewSource(1)))
	var ms uint64
	for _, r := range string(id)[:10] {
		ms = ms<<5 | uint64(strings.IndexRune(crockford, r))
	}
	if ms != 1791379800000 {
		t.Errorf("decoded prefix of %q = %d ms, want 1791379800000", id, ms)
	}
}

func TestParseID(t *testing.T) {
	good := "01J9Z3K8M20000000000000001"
	if id, err := event.ParseID(good); err != nil || string(id) != good {
		t.Fatalf("ParseID(%q) = %q, %v; want it accepted", good, id, err)
	}
	for name, in := range map[string]string{
		"empty":                "",
		"lower case":           strings.ToLower(good),
		"mixed case":           "01j9Z3K8M20000000000000001",
		"letter I":             "01J9Z3K8M2000000000000000I",
		"letter L":             "01J9Z3K8M2000000000000000L",
		"letter O":             "01J9Z3K8M2000000000000000O",
		"letter U":             "01J9Z3K8M2000000000000000U",
		"25 characters":        good[:25],
		"27 characters":        good + "1",
		"space":                "01J9Z3K8M2000000000000000 ",
		"newline":              good[:25] + "\n",
		"first character is 8": "81J9Z3K8M20000000000000001", // would overflow 128 bits
		"non-ASCII":            "01J9Z3K8M2000000000000000é",
	} {
		if id, err := event.ParseID(in); err == nil {
			t.Errorf("%s: ParseID(%q) = %q, want an error", name, in, id)
		}
	}
	if _, err := event.ParseID("7ZZZZZZZZZZZZZZZZZZZZZZZZZ"); err != nil {
		t.Errorf("the largest ULID is rejected: %v", err)
	}
}

func TestNewIDAlwaysParses(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ms := rapid.Int64Range(0, 1<<48-1).Draw(t, "unix milliseconds")
		seed := rapid.Int64().Draw(t, "entropy seed")
		id := event.NewID(time.UnixMilli(ms), rand.New(rand.NewSource(seed)))
		back, err := event.ParseID(string(id))
		if err != nil || back != id {
			t.Fatalf("ParseID(NewID) = %q, %v; want %q", back, err, id)
		}
	})
}

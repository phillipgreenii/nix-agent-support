package main

import "testing"

func TestNeedsInputFingerprint(t *testing.T) {
	got := needsInputFingerprint("sess-1")
	want := "needs-input:sess-1"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestZombieDriftFingerprintEmbedsBand(t *testing.T) {
	cases := []struct {
		band severityZombieBand
		want string
	}{
		{band50Percent, "zombie-count:+50%"},
		{band100Percent, "zombie-count:+100%"},
		{bandSustained, "zombie-count:sustained-growth"},
	}
	for _, c := range cases {
		if got := zombieDriftFingerprint(c.band); got != c.want {
			t.Errorf("zombieDriftFingerprint(%v) = %q, want %q", c.band, got, c.want)
		}
	}
}

// TestZombieDriftFingerprintDiffersAcrossBands proves a band change
// yields a genuinely DIFFERENT fingerprint -- the mechanism dedup.go's
// matchExisting relies on to realize "skip unless moved into a new
// severity band" as a plain create-if-no-match decision.
func TestZombieDriftFingerprintDiffersAcrossBands(t *testing.T) {
	a := zombieDriftFingerprint(band50Percent)
	b := zombieDriftFingerprint(band100Percent)
	if a == b {
		t.Fatalf("expected different fingerprints for different bands, got %q for both", a)
	}
}

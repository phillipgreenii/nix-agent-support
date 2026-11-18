package failsig

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		text string
		want Signature
	}{
		{"fatal: oauth command timed out", GitAuth},
		{"Permission denied (publickey).\nfatal: Could not read from remote repository.", GitAuth},
		{"ssh: Could not resolve hostname h\nfatal: Could not read from remote repository.", GitNetwork},
		{"ssh: connect: Connection refused", GitNetwork},
		{"mkdir /Volumes/x: permission denied", MountOrPath},
		{"chdir /x: no such file or directory", MountOrPath},
		{"something else", Unknown},
		{"", Unknown},
	} {
		if got := Classify(c.text).Signature; got != c.want {
			t.Errorf("%q: %s, want %s", c.text, got, c.want)
		}
	}
}

func TestEvidenceRedactsAndBounds(t *testing.T) {
	long := strings.Repeat("é", 200) + " connection refused https://user:secret@h/r " + strings.Repeat("x", 400)
	r := Classify("first line\n" + long)
	if r.Signature != GitNetwork || len(r.Evidence) > MaxEvidenceLen {
		t.Fatalf("%s len %d", r.Signature, len(r.Evidence))
	}
	if strings.Contains(r.Evidence, "secret") {
		t.Error("evidence leaks userinfo")
	}
	if got := Classify("a\n\nconnection refused\r\n").Evidence; got != "connection refused" {
		t.Errorf("evidence %q", got)
	}
	if nthLine("a", 3) != "" {
		t.Error("nthLine beyond end")
	}
	if got := cut("éé", 1, 10); got != "é" {
		t.Errorf("cut start %q", got)
	}
	if got := cut("éé", 0, 3); got != "é" {
		t.Errorf("cut end %q", got)
	}
}

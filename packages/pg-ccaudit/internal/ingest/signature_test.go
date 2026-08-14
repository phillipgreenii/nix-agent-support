package ingest

import (
	"strings"
	"testing"
)

func TestSignatureNormalization(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "whitespace collapses",
			in:   "a  b\n\tc",
			want: "a b c",
		},
		{
			name: "leading and trailing whitespace trimmed",
			in:   "   padded   ",
			want: "padded",
		},
		{
			name: "git sha becomes HASH",
			in:   "bad object 5ca8f112",
			want: "bad object HASH",
		},
		{
			name: "forty-char sha becomes HASH",
			in:   "rev 0123456789abcdef0123456789abcdef01234567 missing",
			want: "rev HASH missing",
		},
		{
			name: "six hex chars are too short to be a sha",
			in:   "code abcdef here",
			want: "code abcdef here",
		},
		{
			name: "tool use id becomes TOOLID",
			in:   "no result for toolu_01NnMCYE34RVwYJMUT6o8Has",
			want: "no result for TOOLID",
		},
		{
			name: "bead id becomes BEAD",
			in:   "issue pg2-xnnab.9 is claimed",
			want: "issue BEAD is claimed",
		},
		{
			name: "absolute path becomes PATH",
			in:   "File does not exist: /Users/phillipg/repo/x.go",
			want: "File does not exist: PATH",
		},
		{
			name: "a single segment is not a path",
			in:   "closing </tag> only",
			want: "closing </tag> only",
		},
		{
			name: "numbers become N",
			in:   "timed out after 120000 ms, 3 retries",
			want: "timed out after N ms, N retries",
		},
		{
			name: "grouped numbers collapse to one N",
			in:   "1,234.56 rows",
			want: "N rows",
		},
		{
			name: "two occurrences of the same class both collapse",
			in:   "sleep 45 then sleep 90",
			want: "sleep N then sleep N",
		},
		{
			name: "path collapses before numbers so a digit-bearing path is stable",
			in:   "cannot read /tmp/run-123/out.log",
			want: "cannot read PATH",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Signature(tc.in); got != tc.want {
				t.Errorf("Signature(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Two bodies differing only in their volatile parts MUST land on one key —
// otherwise a GROUP BY over signatures counts one recurring problem as many
// unrelated ones, which is exactly the miscount the precomputed column prevents.
func TestSignatureGroupsVolatileVariants(t *testing.T) {
	a := Signature("Read failed: /Users/a/one.go after 12 ms (toolu_AAA111)")
	b := Signature("Read failed: /Users/b/two/three.go after 4500 ms (toolu_BBB222)")
	if a != b {
		t.Fatalf("variants did not group:\n a = %q\n b = %q", a, b)
	}
}

// The window is applied to RUNES. A byte-oriented cut could split a multi-byte
// character and produce an invalid-UTF-8 grouping key — a class of bug the shell
// prototype was vulnerable to and this port must not inherit.
func TestSignatureWindowIsRuneSafe(t *testing.T) {
	body := strings.Repeat("é", SignatureWindow+50)
	got := Signature(body)
	if want := strings.Repeat("é", SignatureWindow); got != want {
		t.Fatalf("window cut %d runes, want %d", len([]rune(got)), SignatureWindow)
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("window produced a replacement character; the cut split a rune")
		}
	}
}

func TestSignatureWindowTruncates(t *testing.T) {
	body := strings.Repeat("x", SignatureWindow+10)
	if got := len(Signature(body)); got != SignatureWindow {
		t.Fatalf("signature length %d, want %d", got, SignatureWindow)
	}
}

func TestSignatureEmpty(t *testing.T) {
	if got := Signature(""); got != "" {
		t.Fatalf("Signature(\"\") = %q", got)
	}
}

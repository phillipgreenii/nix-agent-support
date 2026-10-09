package stranded

import (
	"sort"
	"strings"
	"testing"
)

func scanAll(s string) []string {
	var got []string
	scanClaims([]byte(s), func(v string) { got = append(got, v) })
	sort.Strings(got)
	return got
}

func TestScanClaimsRecognisedShapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"flag then space", `bd update x --actor worker-1 --claim`, []string{"worker-1"}},
		{"flag then tab", "bd update x --actor\tworker-1 ", []string{"worker-1"}},
		{"flag then equals", `bd update x --actor=worker-1 `, []string{"worker-1"}},
		{"flag then double quote", `bd update x --actor "worker-1" `, []string{"worker-1"}},
		{"flag then escaped quote", `bd update x --actor \"worker-1\" `, []string{"worker-1"}},
		{"flag then single quote", `bd update x --actor 'worker-1' `, []string{"worker-1"}},
		{"flag then doubly escaped quote", `bd update x --actor \\\"worker-1\\\" `, []string{"worker-1"}},
		{"env assignment", `BEADS_ACTOR=worker-1 bd ready`, []string{"worker-1"}},
		{"env assignment quoted", `export BEADS_ACTOR="worker-1";`, []string{"worker-1"}},
		{"env assignment escaped quote", `BEADS_ACTOR=\"worker-1\" `, []string{"worker-1"}},
		{"assignee raw", `{"id":"x","assignee":"worker-1","status":"open"}`, []string{"worker-1"}},
		{"assignee raw spaced", `{"assignee": "worker-1"}`, []string{"worker-1"}},
		{"assignee escaped", `{\"assignee\":\"worker-1\"}`, []string{"worker-1"}},
		{"assignee escaped spaced", `{\"assignee\": \"worker-1\"}`, []string{"worker-1"}},
		{"colon and slash and at in a value", `--actor team/bot@host:1 `, []string{"team/bot@host:1"}},
		{"trailing dot and colon trimmed", `--actor worker-1. `, []string{"worker-1"}},
		{"trailing colon trimmed", `--actor worker-1: `, []string{"worker-1"}},
		{"several in one buffer", `--actor a1 BEADS_ACTOR=b2 {"assignee":"c3"}`, []string{"a1", "b2", "c3"}},
		{"assignee key at the very start of the buffer with its quote", `"assignee":"w" `, []string{"w"}},
		{"uuid with suffix", `--actor aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa-drain `, []string{"aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa-drain"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scanAll(tc.in + "\n")
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("scanClaims(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestScanClaimsRejectedShapes(t *testing.T) {
	cases := []struct{ name, in string }{
		{"different flag with the same prefix", `--actors worker-1 `},
		{"flag with a suffix", `--actor-name worker-1 `},
		{"flag glued to its value", `--actorworker-1 `},
		{"flag with no value", `--actor  ` + "\n"},
		{"flag followed by a variable", `--actor "$ACTOR" `},
		{"env assignment with no value", `BEADS_ACTOR= bd ready`},
		{"env name without equals", `BEADS_ACTOR worker-1 `},
		{"assignee null", `{"assignee": null}`},
		{"assignee number", `{"assignee": 7}`},
		{"assignee unquoted", `{"assignee": worker-1}`},
		{"assignee bare word", `assignee: worker-1 `},
		{"assignee as a word inside a key", `{"reassignee":"worker-1"}`},
		{"assignee without a colon", `{"assignee" "worker-1"}`},
		{"assignee at the very start of the buffer", `assignee":"worker-1" `},
		{"value only dots", `--actor ... `},
		{"too long a value", `--actor ` + strings.Repeat("a", maxValueLen+1) + " "},
		{"too long a gap", `--actor ` + strings.Repeat(`"`, maxGapLen+1) + `worker-1 `},
		{"value runs to the end of the buffer", `--actor worker-1`},
		{"gap runs to the end of the buffer", `--actor "`},
		{"flag at the end of the buffer", `--actor`},
		{"bare mention", `bd show worker-1: Assignee: worker-1`},
		{"assignee preceded by a space", `, assignee": "worker-1" `},
		{"assignee preceded by a letter", `x assignee":"worker-1" `},
		{"assignee key ends the buffer", `{"assignee"`},
		{"assignee key then a stray byte instead of a colon", `{"assignee"X"worker-1" `},
		{"assignee key then the byte just below the colon", `{"assignee"9"worker-1" `},
		{"assignee key then the byte just above the colon", `{"assignee";"worker-1" `},
		{"assignee colon then end of buffer", `{"assignee":`},
		{"assignee colon then end after a quote", `{"assignee":"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanAll(tc.in); len(got) != 0 {
				t.Fatalf("scanClaims(%q) = %v, want nothing", tc.in, got)
			}
		})
	}
}

func TestScanClaimsValueLengthBoundary(t *testing.T) {
	exact := strings.Repeat("a", maxValueLen)
	if got := scanAll("--actor " + exact + " "); len(got) != 1 || got[0] != exact {
		t.Fatalf("a value of exactly maxValueLen was not recognised: %v", got)
	}
}

func TestScanClaimsGapLengthBoundary(t *testing.T) {
	// The gap counts every byte between the anchor and the value: the one
	// separator space plus the quotes.
	if got := scanAll("--actor " + strings.Repeat(`"`, maxGapLen-1) + "worker-1 "); len(got) != 1 {
		t.Fatalf("a gap of exactly maxGapLen bytes was not recognised: %v", got)
	}
	if got := scanAll("--actor " + strings.Repeat(`"`, maxGapLen) + "worker-1 "); len(got) != 0 {
		t.Fatalf("a gap of maxGapLen+1 bytes was accepted: %v", got)
	}
}

func TestScanClaimsAssigneeGapLengthBoundary(t *testing.T) {
	// The key's closing quote and the colon count toward the gap, then every
	// quote before the value.
	key := `{"assignee"` + ":"
	if got := scanAll(key + strings.Repeat(`"`, maxGapLen-2) + "worker-1 "); len(got) != 1 {
		t.Fatalf("an assignee gap of exactly maxGapLen bytes was not recognised: %v", got)
	}
	if got := scanAll(key + strings.Repeat(`"`, maxGapLen-1) + "worker-1 "); len(got) != 0 {
		t.Fatalf("an assignee gap of maxGapLen+1 bytes was accepted: %v", got)
	}
}

func TestMaxNeedleLenCoversTheLongestOccurrence(t *testing.T) {
	longest := "x" + anchorActorEnv + strings.Repeat(`"`, maxGapLen) + strings.Repeat("v", maxValueLen) + " "
	if len(longest) != maxNeedleLen {
		t.Fatalf("longest occurrence is %d bytes, maxNeedleLen = %d", len(longest), maxNeedleLen)
	}
	if got := scanAll(longest[1:]); len(got) != 1 {
		t.Fatalf("the longest recognisable occurrence is not recognised: %v", got)
	}
}

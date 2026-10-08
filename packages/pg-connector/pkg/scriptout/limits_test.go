package scriptout

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTruncateForFold_ShortInput_Unchanged(t *testing.T) {
	in := "  boom: something went wrong  "
	got := TruncateForFold([]byte(in))
	want := "boom: something went wrong"
	if got != want {
		t.Fatalf("TruncateForFold(%q) = %q, want %q", in, got, want)
	}
}

func TestTruncateForFold_EmptyInput_ReturnsEmpty(t *testing.T) {
	if got := TruncateForFold(nil); got != "" {
		t.Fatalf("TruncateForFold(nil) = %q, want empty", got)
	}
	if got := TruncateForFold([]byte("   \n\t  ")); got != "" {
		t.Fatalf("TruncateForFold(whitespace-only) = %q, want empty", got)
	}
}

// TestTruncateForFold_LongInput_CappedWithMarker is the core regression
// proof for bead #26: before TruncateForFold existed, every fold call site
// interpolated the FULL captured bytes with no bound at all.
func TestTruncateForFold_LongInput_CappedWithMarker(t *testing.T) {
	huge := strings.Repeat("x", MaxFoldedOutputBytes*3)
	got := TruncateForFold([]byte(huge))
	if len(got) > MaxFoldedOutputBytes+64 {
		t.Fatalf("TruncateForFold output is %d bytes, want capped near MaxFoldedOutputBytes (%d)", len(got), MaxFoldedOutputBytes)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("TruncateForFold output has no truncation marker: %q...(truncated for test log)", got[:200])
	}
	if !strings.HasPrefix(got, strings.Repeat("x", 100)) {
		t.Fatalf("TruncateForFold dropped the LEADING content instead of the trailing overflow")
	}
}

// TestTruncateForFold_RuneBoundarySafe proves a multi-byte UTF-8 sequence
// straddling the byte cap is never split mid-rune (which would otherwise
// leave an invalid/mangled tail byte sequence in the returned string).
func TestTruncateForFold_RuneBoundarySafe(t *testing.T) {
	// A 3-byte rune (e.g. "€", U+20AC) repeated so the cap almost certainly
	// lands inside one of its encoded bytes for at least one padding
	// length; try a small range of paddings to make the boundary case
	// deterministic across MaxFoldedOutputBytes without hardcoding its
	// exact value here.
	for pad := 0; pad < 6; pad++ {
		s := strings.Repeat("a", MaxFoldedOutputBytes-pad) + strings.Repeat("€", 100)
		got := TruncateForFold([]byte(s))
		if !strings.HasSuffix(strings.SplitN(got, "...", 2)[0], "a") && !strings.Contains(got, "€") {
			// Either ending cleanly on the 'a' run or having captured at
			// least one full '€' rune is acceptable; what's NOT
			// acceptable is invalid UTF-8, checked below.
			continue
		}
		body := strings.SplitN(got, "... [truncated", 2)[0]
		if !isValidUTF8(body) {
			t.Fatalf("pad=%d: TruncateForFold produced invalid UTF-8: %q", pad, body)
		}
	}
}

func isValidUTF8(s string) bool {
	return strings.ToValidUTF8(s, "�") == s
}

// TestBackendDeadlineFiresBeforeUmbrella pins bead pg2-5dyz2's staggering: the
// backend's own deadline MUST fire before the umbrella's, by at least
// DefaultWaitDelay, or the umbrella's SIGKILL wins the race again and the
// backend never reports what timed out.
func TestBackendDeadlineFiresBeforeUmbrella(t *testing.T) {
	if DefaultBackendTimeout >= DefaultExecTimeout {
		t.Fatalf("DefaultBackendTimeout %v is not shorter than DefaultExecTimeout %v", DefaultBackendTimeout, DefaultExecTimeout)
	}
	if DefaultExecTimeout-DefaultBackendTimeout < DefaultWaitDelay {
		t.Fatalf("margin %v is smaller than DefaultWaitDelay %v", DefaultExecTimeout-DefaultBackendTimeout, DefaultWaitDelay)
	}
	if backendTimeout != DefaultBackendTimeout || execTimeout != DefaultExecTimeout {
		t.Fatalf("production vars drifted: backendTimeout=%v execTimeout=%v", backendTimeout, execTimeout)
	}
	if listBackendTimeout != ListBackendTimeout || listExecTimeout != ListExecTimeout {
		t.Fatalf("production list vars drifted: listBackendTimeout=%v listExecTimeout=%v", listBackendTimeout, listExecTimeout)
	}
}

// TestPerOpDeadlinesKeepTheKillMargin pins bead pg2-4ae4q's constraint: giving
// list its own, longer budget MUST NOT shrink the margin by which the backend
// answers before the umbrella kills it, for ANY op. It also pins that list's
// budget is a real extension, and that the global budget was not raised.
func TestPerOpDeadlinesKeepTheKillMargin(t *testing.T) {
	for _, op := range []string{OpList, "show", "comment", OpCapabilities, "review_submit"} {
		backend, exec := backendTimeoutFor(op), execTimeoutFor(op)
		if exec-backend != BackendDeadlineMargin {
			t.Errorf("op %q: exec %v - backend %v = %v, want exactly BackendDeadlineMargin %v",
				op, exec, backend, exec-backend, BackendDeadlineMargin)
		}
		if exec-backend < DefaultWaitDelay {
			t.Errorf("op %q: margin %v is smaller than DefaultWaitDelay %v", op, exec-backend, DefaultWaitDelay)
		}
	}
	if ListBackendTimeout <= DefaultBackendTimeout {
		t.Errorf("ListBackendTimeout %v is not longer than DefaultBackendTimeout %v", ListBackendTimeout, DefaultBackendTimeout)
	}
	if got := backendTimeoutFor("show"); got != DefaultBackendTimeout {
		t.Errorf("a non-list op's budget is %v, want the unchanged DefaultBackendTimeout %v", got, DefaultBackendTimeout)
	}
	if got := execTimeoutFor("show"); got != DefaultExecTimeout {
		t.Errorf("a non-list op's exec deadline is %v, want the unchanged DefaultExecTimeout %v", got, DefaultExecTimeout)
	}
}

// TestStaggerDoesNotShrinkTheBackendBudget pins bead pg2-27z7j: the stagger
// margin is added to the umbrella's deadline, never taken out of the backend's
// budget. When pg2-5dyz2 cut the backend to 25s, every call that ran 25-30s
// (pg-connector-pr-github's team search list pages serially, about 27s) was
// killed: its failure rate went from 1.5% to 33%. The budget MUST stay at
// least the 30s every op was sized against, and the umbrella's deadline MUST
// stay the budget plus the margin.
func TestStaggerDoesNotShrinkTheBackendBudget(t *testing.T) {
	const minBudget = 30 * time.Second
	if DefaultBackendTimeout < minBudget {
		t.Fatalf("DefaultBackendTimeout %v is below the %v budget ops are sized against", DefaultBackendTimeout, minBudget)
	}
	if DefaultExecTimeout != DefaultBackendTimeout+BackendDeadlineMargin {
		t.Fatalf("DefaultExecTimeout %v != DefaultBackendTimeout %v + BackendDeadlineMargin %v",
			DefaultExecTimeout, DefaultBackendTimeout, BackendDeadlineMargin)
	}
}

func TestSummarizeArgs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", "{}"},
		{"null", "null", "{}"},
		{"compacts", "{ \"a\": 1,\n \"b\": [1, 2] }", `{"a":1,"b":[1,2]}`},
		{"invalid json passes through trimmed", "  not json ", "not json"},
	}
	for _, c := range cases {
		if got := SummarizeArgs(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("%s: SummarizeArgs(%q) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

func TestSummarizeArgs_CapsLongInputOnRuneBoundary(t *testing.T) {
	for pad := 0; pad < 4; pad++ {
		raw := `{"x":"` + strings.Repeat("a", MaxSummarizedArgsBytes-8-pad) + strings.Repeat("€", 50) + `"}`
		got := SummarizeArgs(json.RawMessage(raw))
		if !strings.Contains(got, "...[+") {
			t.Fatalf("pad %d: no truncation marker in %q", pad, got)
		}
		head := got[:strings.Index(got, "...[+")]
		if len(head) > MaxSummarizedArgsBytes {
			t.Fatalf("pad %d: head is %d bytes, over the cap", pad, len(head))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("pad %d: invalid UTF-8 in %q", pad, got)
		}
	}
}

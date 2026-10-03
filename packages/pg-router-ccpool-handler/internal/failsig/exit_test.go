package failsig

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func facts(state string, live, present bool) ExitFacts {
	return ExitFacts{Observed: true, Present: present, State: state, Live: live}
}

// TestClassifyExit_shapes covers each common exit shape: the synthetic
// transcript tail plus the session facts observed at exit, and the signature
// they MUST get. Fixtures are synthetic; the API-error texts follow the shapes
// Claude Code writes (see the table.go header).
func TestClassifyExit_shapes(t *testing.T) {
	cases := []struct {
		name       string
		transcript string
		facts      ExitFacts
		want       Signature
	}{
		{"api 500", "worked\nAPI Error: 500 Internal server error. This is a server-side issue.", facts("errored", false, true), APITransient},
		{"api 529 overloaded", "API Error: 529 Overloaded", facts("errored", true, true), APITransient},
		{"socket closed", "API Error: The socket connection was closed unexpectedly.", facts("errored", false, true), APITransient},
		{"unable to connect", "Unable to connect to API (FailedToOpenSocket)", facts("errored", false, true), APITransient},
		{"stream idle", "Stream idle timeout - partial response received", facts("errored", false, true), APITransient},
		{"rate limit", "You've hit your limit · resets 3:30pm (America/New_York)", facts("errored", false, true), APIRateLimit},
		{"auth 401", "Please run /login · API Error: 401 Invalid authentication credentials", facts("errored", false, true), APIAuth},
		{"context limit", "Prompt is too long: 215000 tokens > 200000 maximum", facts("errored", false, true), ContextLimit},
		{"api error outranks earlier git failure", "fatal: Could not read from remote repository.\nAPI Error: 529 Overloaded", facts("errored", false, true), APITransient},
		{"git failure still classified over facts", "error: oauth command timed out", facts("errored", false, true), GitAuth},
		{"errored, no text", "doing work\nstill working", facts("errored", false, true), SessionErrored},
		{"errored, empty transcript", "", facts("errored", true, true), SessionErrored},
		{"idle, bead not completed", "I think that is done.", facts("idle", true, true), SessionIdle},
		{"dead pane", "doing work", facts("working", false, true), SessionGone},
		{"row absent", "doing work", facts("working", false, false), SessionGone},
		{"alive and working (timeout path)", "doing work", facts("working", true, true), Unknown},
		{"never observed", "doing work", ExitFacts{}, Unknown},
		{"success text but errored", "Done. Task completed successfully.", facts("errored", false, true), SessionErrored},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyExit(c.transcript, c.facts)
			if got.Signature != c.want {
				t.Fatalf("Signature = %q, want %q (evidence %q)", got.Signature, c.want, got.Evidence)
			}
			if !got.Signature.Valid() {
				t.Errorf("Signature %q is not in the closed set", got.Signature)
			}
			if got.Evidence == "" || len(got.Evidence) > MaxEvidenceLen || !utf8.ValidString(got.Evidence) {
				t.Errorf("Evidence = %q, want non-empty, <= %d bytes, valid UTF-8", got.Evidence, MaxEvidenceLen)
			}
		})
	}
}

// An exit no transcript row explains records the facts line and the last
// transcript lines, so the evidence is never empty, even for Unknown and even
// with an empty transcript.
func TestClassifyExit_fallbackEvidenceIsFactsAndTail(t *testing.T) {
	transcript := "line one\nline two\n\nline three\nline four\n"
	got := ClassifyExit(transcript, ExitFacts{Observed: true, Present: true, State: "errored", CloseReason: "operator"})
	want := "ccpool-session: state=errored live=false present=true close_reason=operator | tail: line two / line three / line four"
	if got.Evidence != want {
		t.Errorf("Evidence = %q, want %q", got.Evidence, want)
	}

	empty := ClassifyExit("", ExitFacts{})
	if empty.Signature != Unknown || empty.Evidence != "ccpool-session: not-observed" {
		t.Errorf("empty transcript, nothing observed = %+v, want unknown with the facts line", empty)
	}
}

// A matched row keeps Classify's evidence: the matched line, not the tail.
func TestClassifyExit_matchedRowEvidenceIsTheMatchedLine(t *testing.T) {
	got := ClassifyExit("before\n  API Error: 529 Overloaded  \nafter", facts("errored", false, true))
	if got.Signature != APITransient || got.Evidence != "API Error: 529 Overloaded" {
		t.Errorf("got %+v, want api-transient with the matched line only", got)
	}
}

// Fallback evidence is redacted before it is cut, and bounded.
func TestClassifyExit_fallbackEvidenceRedactedAndBounded(t *testing.T) {
	token := "ghp_" + strings.Repeat("A1b2C3d4E5", 4)
	long := strings.Repeat("é x ", 200)
	transcript := "first https://bob:s3cretpw@example.com/org/repo.git\n" + long + "\ntoken " + token + "\n"
	got := ClassifyExit(transcript, facts("errored", false, true))
	if got.Signature != SessionErrored {
		t.Fatalf("Signature = %q, want session-errored", got.Signature)
	}
	for _, leak := range []string{token, "s3cretpw", "bob:"} {
		if strings.Contains(got.Evidence, leak) {
			t.Errorf("Evidence %q leaks %q", got.Evidence, leak)
		}
	}
	if len(got.Evidence) > MaxEvidenceLen || !utf8.ValidString(got.Evidence) {
		t.Errorf("Evidence len %d valid=%v, want <= %d and valid UTF-8", len(got.Evidence), utf8.ValidString(got.Evidence), MaxEvidenceLen)
	}
}

func TestExitFactsLine(t *testing.T) {
	cases := []struct {
		f    ExitFacts
		want string
	}{
		{ExitFacts{}, "ccpool-session: not-observed"},
		{ExitFacts{Observed: true}, "ccpool-session: state=none live=false present=false close_reason=none"},
		{ExitFacts{Observed: true, Present: true, State: "idle", Live: true, CloseReason: "idle_ttl"}, "ccpool-session: state=idle live=true present=true close_reason=idle_ttl"},
	}
	for _, c := range cases {
		if got := c.f.line(); got != c.want {
			t.Errorf("line() = %q, want %q", got, c.want)
		}
	}
}

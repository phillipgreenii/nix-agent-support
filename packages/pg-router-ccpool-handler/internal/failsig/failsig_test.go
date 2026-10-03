package failsig

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// positives maps each table row's name to texts that MUST be classified by
// THAT row. The row must be the first match, not merely some later row that
// also matches. The first entry of each row is the exact string the row
// exists for (bead pg2-jw9rr's required rows), where the bead names one.
var positives = map[string][]string{
	"session-budget-exceeded": {
		"session budget exceeded",
		"watchdog: session budget exceeded at 100% of the configured limit",
		// Budget outranks everything: a hard-stopped session whose
		// transcript also shows a git failure is labeled budget.
		"fatal: Could not read from remote repository.\nsession budget exceeded",
	},
	"oauth-command-timed-out": {
		"oauth command timed out",
		"error: oauth command timed out\nfatal: Could not read from remote repository.",
	},
	"oidc-token-error": {
		`error generating OIDC token: exec "step oauth" failed`,
	},
	"step-oauth-exec-failed": {
		`exec "step oauth" failed: exit status 1`,
	},
	"ssh-publickey-denied": {
		"Permission denied (publickey)",
		"git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.\n\n" +
			"Please make sure you have the correct access rights\nand the repository exists.",
		"user@host: Permission denied (publickey,keyboard-interactive).",
	},
	"could-not-resolve-host": {
		"Could not resolve host",
		"fatal: unable to access 'https://github.com/org/repo.git/': Could not resolve host: github.com",
		// Network outranks the generic "Could not read from remote
		// repository" line that git prints after every failed ssh
		// transport.
		"ssh: Could not resolve hostname github.com: nodename nor servname provided, or not known\n" +
			"fatal: Could not read from remote repository.",
	},
	"connection-timed-out": {
		"Connection timed out",
		"ssh: connect to host github.com port 22: Connection timed out\nfatal: Could not read from remote repository.",
	},
	"connection-refused": {
		"Connection refused",
		"ssh: connect to host git.example.com port 22: Connection refused\nfatal: Could not read from remote repository.",
	},
	"could-not-read-from-remote": {
		"Could not read from remote repository",
		"fatal: Could not read from remote repository.\n\nPlease make sure you have the correct access rights",
	},
	"mkdir-permission-denied": {
		"mkdir /Volumes/gitrepos: permission denied",
		"prepare isolation: mkdir /Volumes/gitrepos/wt: permission denied",
	},
	"path-no-such-file": {
		"chdir /Volumes/gitrepos/repo: no such file or directory",
		"fatal: cannot change to '/Volumes/gitrepos/repo': No such file or directory",
		"git worktree add /Volumes/gitrepos/wt/x: exit status 128: fatal: could not create leading directories " +
			"of '/Volumes/gitrepos/wt/x': No such file or directory",
		"mkdir /Volumes/gitrepos/wt/x: no such file or directory",
	},
	"index-lock": {
		"index.lock",
		"fatal: Unable to create '/repo/.git/index.lock': File exists.\n\n" +
			"Another git process seems to be running in this repository",
	},
	"prompt-too-long": {
		"Prompt is too long",
		"API Error: Prompt is too long: 215000 tokens > 200000 maximum",
	},
	"usage-limit-hit": {
		"You've hit your limit · resets 7:10pm (America/New_York)",
		"You\u2019ve hit your usage limit",
	},
	"api-error-429": {
		"API Error: 429 Too Many Requests",
	},
	"please-run-login": {
		"Not logged in · Please run /login",
		"Please run /login · API Error: 401 Invalid authentication credentials",
	},
	"api-error-401": {
		"API Error: 401 Invalid authentication credentials",
	},
	"api-error-5xx": {
		"API Error: 500 Internal server error. This is a server-side issue, usually temporary — try again in a moment.",
		"API Error: 529 Overloaded",
		"API Error: 502 Bad Gateway",
	},
	"api-transport-drop": {
		"API Error: The socket connection was closed unexpectedly.",
		// Outranks git-network's "connection refused" even when both appear.
		"API Error: Unable to connect to API (ConnectionRefused)",
		"Unable to connect to API (ECONNRESET)",
		"Stream idle timeout - partial response received",
	},
	"api-bare-server-error": {
		"Overloaded",
		"some output\n  Internal server error.  \nmore output",
	},
	"exit-state-errored": {
		"ccpool-session: state=errored live=false present=true close_reason=none",
		// Transcript text that matches no row does not outrank the facts.
		"all quiet\nccpool-session: state=errored live=true present=true close_reason=none",
	},
	"exit-state-idle": {
		"ccpool-session: state=idle live=true present=true close_reason=none",
	},
	"exit-session-gone": {
		"ccpool-session: state=working live=false present=true close_reason=none",
		"ccpool-session: state=none live=false present=false close_reason=none",
	},
}

// negatives maps each table row's name to near-miss texts for that row:
// success output, or prose that uses the row's words. Each one MUST
// classify as Unknown overall, not just miss that one row.
var negatives = map[string][]string{
	"session-budget-exceeded": {
		"session budget: 42% used",
		"the session stayed within its budget; nothing was exceeded",
	},
	"oauth-command-timed-out": {
		"Configured the oauth client; the command finished in 2s.",
		"oauth token refreshed, command completed",
	},
	"oidc-token-error": {
		"generated OIDC token for the step CLI",
		"OIDC login succeeded",
	},
	"step-oauth-exec-failed": {
		`exec "step oauth" succeeded`,
		"ran step oauth to refresh the ssh certificate",
	},
	"ssh-publickey-denied": {
		`Authenticated to github.com using "publickey".`,
		"Hi there! You've successfully authenticated, but GitHub does not provide shell access.",
	},
	"could-not-resolve-host": {
		"Resolved host github.com to 192.0.2.10",
		"resolving host names is handled by the resolver",
	},
	"connection-timed-out": {
		"Connection to github.com port 22 [tcp/ssh] succeeded!",
		"the connection was established before the timeout",
	},
	"connection-refused": {
		"connection accepted from 127.0.0.1",
		"no connection was refused today",
	},
	"could-not-read-from-remote": {
		"Read 42 objects from remote repository origin",
		"From github.com:org/repo\n * branch            main       -> FETCH_HEAD\nAlready up to date.",
	},
	"mkdir-permission-denied": {
		"mkdir -p /Volumes/gitrepos/x && echo created",
		"open /etc/hosts: permission denied",
	},
	"path-no-such-file": {
		"open config.toml: no such file or directory",
		`exec: "claude": executable file not found in $PATH`,
		"chdir /Volumes/gitrepos/repo: ok",
	},
	"index-lock": {
		"[main 1a2b3c4] fix: thing\n 1 file changed, 2 insertions(+)",
		"the index was locked briefly and then released",
	},
	"prompt-too-long": {
		"the prompt was short enough to fit",
		"a long prompt is fine here",
	},
	"usage-limit-hit": {
		"You've hit the target; the limit was not reached",
		"the usage limit was raised to 500",
	},
	"api-error-429": {
		"API Error: 4290 is not a status",
		"returned 429 from the mirror, retried and succeeded",
	},
	"please-run-login": {
		"run /login to authenticate",
		"login succeeded; please run the tests",
	},
	"api-error-401": {
		"API Error: 4010 is not a status",
		"the proxy answered 401 once and then accepted the token",
	},
	"api-error-5xx": {
		"API Error: 200 OK",
		"API Error: 5000 is not a status",
	},
	"api-transport-drop": {
		"the socket was opened and stayed open",
		"the stream finished well inside the idle limit",
	},
	"api-bare-server-error": {
		"Overloaded functions are covered in chapter 3",
		"internal server error handling was reviewed in the design doc",
	},
	"exit-state-errored": {
		"state=errored",
		"ccpool-session: state=working live=true present=true close_reason=none",
		"ccpool-session: not-observed",
	},
	"exit-state-idle": {
		"state=idle live=true",
		"the session is idle",
	},
	"exit-session-gone": {
		"live=false",
		"ccpool-session: state=working live=true present=true close_reason=none",
	},
}

func TestEveryRowHasPositiveAndNegativeCases(t *testing.T) {
	names := map[string]bool{}
	for _, r := range table {
		if names[r.name] {
			t.Errorf("duplicate row name %q", r.name)
		}
		names[r.name] = true
		if len(positives[r.name]) == 0 {
			t.Errorf("row %q has no positive case", r.name)
		}
		if len(negatives[r.name]) == 0 {
			t.Errorf("row %q has no negative case", r.name)
		}
	}
	for name := range positives {
		if !names[name] {
			t.Errorf("positives has a case for %q, which is not a table row", name)
		}
	}
	for name := range negatives {
		if !names[name] {
			t.Errorf("negatives has a case for %q, which is not a table row", name)
		}
	}
}

func TestPositiveCasesHitTheirRow(t *testing.T) {
	for name, texts := range positives {
		for _, text := range texts {
			r, start, ok := firstMatch(text)
			if !ok || r.name != name {
				t.Errorf("%q: first matching row = %q (matched=%v), want %q", text, r.name, ok, name)
				continue
			}
			loc := r.re.FindStringIndex(text)
			if strings.Contains(text[loc[0]:loc[1]], "\n") {
				t.Errorf("row %q matched across a newline in %q; rows must match within one line", name, text)
			}
			got := Classify(text)
			if got.Signature != r.signature {
				t.Errorf("Classify(%q).Signature = %q, want %q", text, got.Signature, r.signature)
			}
			if got.Evidence == "" || len(got.Evidence) > MaxEvidenceLen {
				t.Errorf("Classify(%q).Evidence = %q, want non-empty and <= %d bytes", text, got.Evidence, MaxEvidenceLen)
			}
			if want := nthLine(text, strings.Count(text[:start], "\n")); got.Evidence != strings.TrimSpace(want) {
				t.Errorf("Classify(%q).Evidence = %q, want the matched line %q", text, got.Evidence, want)
			}
		}
	}
}

func TestNegativeCasesAreUnknown(t *testing.T) {
	for name, texts := range negatives {
		for _, text := range texts {
			if got := Classify(text); got.Signature != Unknown || got.Evidence != "" {
				t.Errorf("row %q near-miss %q: Classify = %+v, want {unknown, \"\"}", name, text, got)
			}
		}
	}
}

// The exact strings bead pg2-jw9rr requires, each mapped to its signature
// through the public API only.
func TestRequiredStringsClassify(t *testing.T) {
	cases := []struct {
		in   string
		want Signature
	}{
		{"oauth command timed out", GitAuth},
		{`error generating OIDC token: exec "step oauth" failed`, GitAuth},
		{"Permission denied (publickey)", GitAuth},
		{"Could not read from remote repository", GitAuth},
		{"mkdir /Volumes/gitrepos: permission denied", MountOrPath},
		{"chdir /Volumes/gitrepos/repo: no such file or directory", MountOrPath},
		{"git worktree add /Volumes/gitrepos/wt: no such file or directory", MountOrPath},
		{"Could not resolve host", GitNetwork},
		{"Connection timed out", GitNetwork},
		{"Connection refused", GitNetwork},
		{"index.lock", IndexLock},
		{"Unable to create '/repo/.git/index.lock': File exists", IndexLock},
		{"session budget exceeded", Budget},
	}
	for _, c := range cases {
		if got := Classify(c.in).Signature; got != c.want {
			t.Errorf("Classify(%q).Signature = %q, want %q", c.in, got, c.want)
		}
	}
}

// The word "oauth" in unrelated prose MUST NOT classify as git-auth.
func TestOauthInProseIsNotGitAuth(t *testing.T) {
	for _, text := range []string{
		"oauth",
		"Set up the OAuth app for the dashboard.",
		"the oauth command is configured in ~/.config/step",
		"OAuth token rotated successfully; the command timed nothing out.",
	} {
		if got := Classify(text).Signature; got != Unknown {
			t.Errorf("Classify(%q).Signature = %q, want unknown", text, got)
		}
	}
}

// A process can exit non-zero even though its text shows success. Only
// the text is classified, so such text MUST be unknown, not a failure
// signature.
func TestSuccessTextIsUnknown(t *testing.T) {
	for _, text := range []string{
		"",
		"Everything up-to-date",
		"To github.com:org/repo.git\n   1a2b3c4..5d6e7f8  main -> main",
		"Successfully rebased and updated refs/heads/main.",
		"All tests passed.\nok  \texample.com/pkg\t0.123s",
		"Committed and pushed; the session finished its work.",
	} {
		if got := Classify(text); got.Signature != Unknown || got.Evidence != "" {
			t.Errorf("Classify(%q) = %+v, want {unknown, \"\"}", text, got)
		}
	}
}

func TestSignaturesIsClosedSet(t *testing.T) {
	want := []Signature{
		"git-auth", "git-network", "mount-or-path", "budget", "index-lock",
		"api-transient", "api-rate-limit", "api-auth", "context-limit",
		"session-errored", "session-idle", "session-gone",
		"unknown",
	}
	got := Signatures()
	if !slices.Equal(got, want) {
		t.Fatalf("Signatures() = %v, want exactly %v", got, want)
	}
	got[0] = "mutated"
	if Signatures()[0] != GitAuth {
		t.Error("Signatures() must return a copy; mutating it changed the closed set")
	}
	for _, s := range want {
		if !s.Valid() || s.String() != string(s) {
			t.Errorf("%q: Valid()=%v String()=%q", s, s.Valid(), s.String())
		}
	}
	for _, s := range []Signature{"", "git_auth", "Git-Auth", "network"} {
		if s.Valid() {
			t.Errorf("Signature(%q).Valid() = true, want false", s)
		}
	}
}

func TestRowsUseClosedSetAndNeverUnknown(t *testing.T) {
	for _, r := range table {
		if !r.signature.Valid() || r.signature == Unknown {
			t.Errorf("row %q has signature %q; a row must name a closed-set member other than unknown", r.name, r.signature)
		}
	}
}

func TestEvidenceIsTheMatchedLine(t *testing.T) {
	text := "Cloning into 'repo'...\n  fatal: Could not resolve host: github.com  \nsomething after"
	got := Classify(text)
	if got.Signature != GitNetwork || got.Evidence != "fatal: Could not resolve host: github.com" {
		t.Errorf("Classify = %+v, want git-network with only the trimmed matched line", got)
	}
}

func TestEvidenceIsBoundedAndWindowedOnTheMatch(t *testing.T) {
	// Spaces keep the filler out of the generic-run redaction. The
	// multi-byte filler makes a byte-offset cut land mid-rune unless cut
	// moves it to a boundary.
	filler := strings.Repeat("é x ", 150)
	text := filler + "ssh: connect to host h port 22: Connection refused " + filler
	got := Classify(text)
	if got.Signature != GitNetwork {
		t.Fatalf("Signature = %q, want git-network", got.Signature)
	}
	if len(got.Evidence) > MaxEvidenceLen {
		t.Errorf("len(Evidence) = %d, want <= %d", len(got.Evidence), MaxEvidenceLen)
	}
	if !utf8.ValidString(got.Evidence) {
		t.Errorf("Evidence is not valid UTF-8 (a cut split a rune): %q", got.Evidence)
	}
	if !strings.Contains(got.Evidence, "Connection refused") {
		t.Errorf("Evidence %q lost the matched text; the window must keep the match", got.Evidence)
	}
}

// A multi-line private-key block ABOVE the failure line is masked to a
// single marker. Redaction keeps its newlines, so the evidence is still the
// failure line, not a blank or shifted one.
func TestEvidenceLineSurvivesMultilineRedaction(t *testing.T) {
	text := rsaKey + "\nfatal: Could not resolve host: github.com\n"
	got := Classify(text)
	if got.Evidence != "fatal: Could not resolve host: github.com" {
		t.Errorf("Evidence = %q, want the failure line", got.Evidence)
	}
}

// Redaction can remove part of the matched marker: here a long worktree
// path turns ".../index" into [REDACTED]. Classification still ran on the
// raw text, so the signature holds. The evidence is the redacted line,
// windowed from its start.
func TestEvidenceWhenRedactionRemovesTheMarker(t *testing.T) {
	text := "fatal: Unable to create '/c/.git/worktrees/drain-a-rather-long-worktree-name/index.lock': File exists."
	got := Classify(text)
	if got.Signature != IndexLock {
		t.Fatalf("Signature = %q, want index-lock", got.Signature)
	}
	want := "fatal: Unable to create '/c/.[REDACTED].lock': File exists."
	if got.Evidence != want {
		t.Errorf("Evidence = %q, want %q", got.Evidence, want)
	}
}

func TestEvidenceIsRedacted(t *testing.T) {
	for _, c := range redactCases {
		text := "fatal: Could not read from remote repository: " + c.in
		ev := Classify(text).Evidence
		for _, s := range c.secrets {
			if strings.Contains(ev, s) {
				t.Errorf("%s: Evidence %q leaks %q", c.name, ev, s)
			}
		}
	}
}

// Redaction MUST run before truncation. The token here starts before the
// MaxEvidenceLen boundary and ends after it. If the text were cut first, the
// kept fragment would be too short for any pattern to recognize, so it would
// leak. Each case also runs that wrong order as a control, to prove the
// fixture really does straddle the cut in a way that leaks.
func TestRedactionPrecedesTruncation(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"github-token", "ghp_" + "Z9Y8X7W6V5U4T3S2R1Q0PZ9Y8X7W6V5U4T3S"},
		{"hex-run", "0123456789abcdef0123456789abcdef01234567"},
	}
	const head = "fatal: Could not read from remote repository. "
	const tokenStart = MaxEvidenceLen - 10
	for _, c := range cases {
		pad := strings.Repeat("b ", (tokenStart-len(head))/2)
		line := head + pad + c.token + " trailing context"
		if got := strings.Index(line, c.token); got >= MaxEvidenceLen || got+len(c.token) <= MaxEvidenceLen {
			t.Fatalf("%s: fixture token spans [%d,%d), want it to straddle %d", c.name, got, got+len(c.token), MaxEvidenceLen)
		}
		fragment := c.token[:6]

		if wrong := Redact(line[:MaxEvidenceLen]); !strings.Contains(wrong, fragment) {
			t.Fatalf("%s: control failed: cut-then-redact did not leak %q, so this fixture proves nothing", c.name, fragment)
		}

		got := Classify(line)
		if got.Signature != GitAuth {
			t.Fatalf("%s: Signature = %q, want git-auth", c.name, got.Signature)
		}
		if strings.Contains(got.Evidence, fragment) {
			t.Errorf("%s: Evidence %q leaks token fragment %q; redaction must precede truncation", c.name, got.Evidence, fragment)
		}
		if len(got.Evidence) > MaxEvidenceLen {
			t.Errorf("%s: len(Evidence) = %d, want <= %d", c.name, len(got.Evidence), MaxEvidenceLen)
		}
	}
}

func TestNthLine(t *testing.T) {
	s := "zero\none\ntwo"
	for n, want := range []string{"zero", "one", "two", ""} {
		if got := nthLine(s, n); got != want {
			t.Errorf("nthLine(%q, %d) = %q, want %q", s, n, got, want)
		}
	}
}

func TestCutKeepsRuneBoundaries(t *testing.T) {
	s := "aé" + strings.Repeat("é", 10) // "é" is 2 bytes
	cases := []struct {
		start, maxLen int
		want          string
	}{
		{0, 100, s},
		{0, 2, "a"},     // byte 2 is mid-"é": end moves back
		{2, 4, "éé"},    // byte 2 is mid-"é": start moves forward
		{0, 3, "aé"},    // exactly on a boundary
		{len(s), 5, ""}, // start at the end
	}
	for _, c := range cases {
		got := cut(s, c.start, c.maxLen)
		if got != c.want || !utf8.ValidString(got) {
			t.Errorf("cut(%q, %d, %d) = %q, want %q", s, c.start, c.maxLen, got, c.want)
		}
	}
}

package failsig

import (
	"strings"
	"testing"
)

// Private-key fixtures. The bodies are shaped like key material but are
// random filler, not real keys.
const (
	rsaKey = "-----BEGIN RSA PRIVATE KEY-----\n" +
		"MIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gun\n" +
		"VTLw7onLRnrq0/IzW7yWR7QkrmBL7jTKEn5u+qKhbwKfBstIs+bMY2Zkp18gnTxK\n" +
		"Qx9Kc\n" +
		"-----END RSA PRIVATE KEY-----"
	opensshKey = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n" +
		"QyNTUxOQAAACDk2mJ8f3kq\n" +
		"-----END OPENSSH PRIVATE KEY-----"
)

// redactCases is one or more cases per redaction pattern. secrets lists
// substrings that MUST NOT survive; want, when set, is the exact expected
// output; pass names the redaction pass that must mask the secret ON ITS
// OWN, so a later catch-all can never hide a broken specific pattern.
var redactCases = []struct {
	name    string
	pass    string
	in      string
	want    string
	secrets []string
}{
	{
		name:    "userinfo user and password",
		pass:    "url-userinfo",
		in:      "fatal: unable to access 'https://oauth2:s3cretT0ken@gitlab.example.com/g/r.git/': error: 403",
		want:    "fatal: unable to access 'https://[REDACTED]@gitlab.example.com/g/r.git/': error: 403",
		secrets: []string{"oauth2", "s3cretT0ken"},
	},
	{
		name:    "userinfo token as username",
		pass:    "url-userinfo",
		in:      "git clone https://tok3nAsUser@github.com/o/r",
		want:    "git clone https://[REDACTED]@github.com/o/r",
		secrets: []string{"tok3nAsUser"},
	},
	{
		name:    "userinfo password containing a raw @",
		pass:    "url-userinfo",
		in:      "remote: https://user:p@ssw0rd@host.example/x",
		want:    "remote: https://[REDACTED]@host.example/x",
		secrets: []string{"p@ss", "ssw0rd"},
	},
	{
		name:    "bearer token",
		pass:    "bearer-token",
		in:      "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl",
		want:    "Authorization: Bearer [REDACTED]",
		secrets: []string{"eyJhbGciOiJIUzI1NiJ9"},
	},
	{
		name:    "bearer lowercase",
		pass:    "bearer-token",
		in:      "authorization: bearer abc123shortTok",
		want:    "authorization: bearer [REDACTED]",
		secrets: []string{"abc123shortTok"},
	},
	{
		name:    "ghp_ token",
		pass:    "prefixed-token",
		in:      "token=" + fake("ghp_", "a1B2c3D4e5F6g7H8") + " rest",
		want:    "token=[REDACTED] rest",
		secrets: []string{"a1B2c3D4e5F6g7H8"},
	},
	{
		name:    "gho_ token",
		pass:    "prefixed-token",
		in:      fake("gho_", "Q1w2E3r4T5y6U7i8O9p0") + " was rejected",
		want:    "[REDACTED] was rejected",
		secrets: []string{"Q1w2E3r4T5y6U7i8O9p0"},
	},
	{
		name:    "github_pat_ token",
		pass:    "prefixed-token",
		in:      "GH_TOKEN=" + fake("github_pat_", "11ABCDEFG0_xyzXYZ123"),
		want:    "GH_TOKEN=[REDACTED]",
		secrets: []string{"11ABCDEFG0_xyzXYZ123"},
	},
	{
		name:    "glpat- token",
		pass:    "prefixed-token",
		in:      "PRIVATE-TOKEN: " + fake("glpat-", "Ab12Cd34Ef56Gh78Ij90"),
		want:    "PRIVATE-TOKEN: [REDACTED]",
		secrets: []string{"Ab12Cd34Ef56Gh78Ij90"},
	},
	{
		name:    "AKIA access key id",
		pass:    "prefixed-token",
		in:      "aws_access_key_id = AKIAIOSFODNN7EXAMPLE",
		want:    "aws_access_key_id = [REDACTED]",
		secrets: []string{"IOSFODNN7EXAMPLE"},
	},
	{
		name:    "sk- API key",
		pass:    "prefixed-token",
		in:      "api key " + fake("sk-", "proj-Zx9Yw8Vu7Ts6Rq5P") + " invalid",
		want:    "api key [REDACTED] invalid",
		secrets: []string{"Zx9Yw8Vu7Ts6Rq5P"},
	},
	{
		name:    "32-char hex run",
		pass:    "generic-run",
		in:      "secret=0123456789abcdef0123456789abcdef",
		want:    "secret=[REDACTED]",
		secrets: []string{"0123456789abcdef"},
	},
	{
		name:    "base64 run with padding",
		pass:    "generic-run",
		in:      "value: dGhpcyBpcyBhIHNlY3JldCB2YWx1ZSBmb3IgdGVzdHM= end",
		want:    "value: [REDACTED] end",
		secrets: []string{"dGhpcyBpcyBh", "="},
	},
	{
		name:    "base64 run with / and +",
		pass:    "generic-run",
		in:      "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		want:    "aws_secret_access_key = [REDACTED]",
		secrets: []string{"wJalrXUtnFEMI", "K7MDENG", "bPxRfiCY"},
	},
	{
		name:    "RSA private-key block",
		pass:    "private-key-block",
		in:      "key:\n" + rsaKey + "\nafter",
		want:    "key:\n[REDACTED]\n\n\n\n\nafter",
		secrets: []string{"MIIEowIBAAKCAQEA", "Qx9Kc", "PRIVATE KEY"},
	},
	{
		name:    "OpenSSH private-key block",
		pass:    "private-key-block",
		in:      opensshKey,
		want:    "[REDACTED]\n\n\n",
		secrets: []string{"b3BlbnNzaC1rZXkt", "QyNTUxOQAAACDk2mJ8f3kq", "PRIVATE KEY"},
	},
	{
		name:    "key block cut before its END line",
		pass:    "private-key-block",
		in:      "ok\n" + rsaKey[:strings.Index(rsaKey, "-----END")],
		want:    "ok\n[REDACTED]\n\n\n\n",
		secrets: []string{"MIIEowIBAAKCAQEA", "Qx9Kc"},
	},
	{
		name:    "key block cut before its BEGIN line",
		pass:    "private-key-orphan-end",
		in:      "tail of a transcript\nQyNTUxOQAAACDk2mJ8f3kq\nAbc12\n-----END OPENSSH PRIVATE KEY-----\nnext line",
		want:    "tail of a transcript\n[REDACTED]\n\n\nnext line",
		secrets: []string{"QyNTUxOQAAACDk2mJ8f3kq", "Abc12"},
	},
}

// fake joins a token prefix and body at runtime so no token-shaped literal sits in
// the source for secret scanners (e.g. GitHub push protection) to flag.
func fake(prefix, body string) string { return prefix + body }

func TestRedactEachPattern(t *testing.T) {
	for _, c := range redactCases {
		got := Redact(c.in)
		if c.want != "" && got != c.want {
			t.Errorf("%s: Redact(%q)\n got %q\nwant %q", c.name, c.in, got, c.want)
		}
		for _, s := range c.secrets {
			if strings.Contains(got, s) {
				t.Errorf("%s: Redact output %q still contains %q", c.name, got, s)
			}
		}
		if !strings.Contains(got, Marker) {
			t.Errorf("%s: Redact output %q has no %s marker", c.name, got, Marker)
		}
	}
}

// Every pattern must mask its own secrets without help from any other pass.
// Otherwise the generic-run catch-all could hide a broken specific pattern
// for tokens of 32 or more characters, and the pattern would be dead
// weight for shorter ones.
func TestEachPassMasksItsOwnCase(t *testing.T) {
	byName := map[string]redaction{}
	for _, r := range redactions {
		byName[r.name] = r
	}
	covered := map[string]bool{}
	for _, c := range redactCases {
		r, ok := byName[c.pass]
		if !ok {
			t.Fatalf("%s: no redaction pass named %q", c.name, c.pass)
		}
		covered[c.pass] = true
		got := r.apply(c.in)
		for _, s := range c.secrets {
			if strings.Contains(got, s) {
				t.Errorf("%s: pass %q alone left %q in %q", c.name, c.pass, s, got)
			}
		}
	}
	for _, r := range redactions {
		if !covered[r.name] {
			t.Errorf("redaction pass %q has no test case", r.name)
		}
	}
}

// Classify's evidence line mapping depends on this property.
func TestRedactPreservesNewlineCount(t *testing.T) {
	for _, c := range redactCases {
		if got, want := strings.Count(Redact(c.in), "\n"), strings.Count(c.in, "\n"); got != want {
			t.Errorf("%s: Redact changed the newline count from %d to %d", c.name, want, got)
		}
	}
}

func TestGenericRunThreshold(t *testing.T) {
	run31 := strings.Repeat("a1", 15) + "b"
	if got := Redact("x " + run31 + " y"); got != "x "+run31+" y" {
		t.Errorf("a 31-char run was redacted: %q", got)
	}
	run32 := run31 + "c"
	if got := Redact("x " + run32 + " y"); got != "x [REDACTED] y" {
		t.Errorf("a 32-char run was not redacted: %q", got)
	}
}

// Ordinary failure text, and words that merely contain a token prefix,
// pass through untouched.
func TestRedactLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{
		"fatal: Could not read from remote repository.",
		"mkdir /Volumes/gitrepos: permission denied",
		"ssh: connect to host github.com port 22: Connection timed out",
		"git@github.com: Permission denied (publickey).",
		"the task-runner and disk-cleanup jobs ran",
		"--------------------------------------------------",
		"the bearer of bad news",
		"",
	} {
		if got := Redact(s); got != s {
			t.Errorf("Redact(%q) = %q, want it unchanged", s, got)
		}
	}
}

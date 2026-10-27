package curl

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

// decide runs one command through a rule built with an evaluator rooted at /tmp, so
// the -o checks have a real write zone (/tmp) and a real non-write zone (/etc).
func decide(t *testing.T, cmd string) hookio.Decision {
	t.Helper()
	pe := patheval.NewWithCWD("/tmp", "/tmp")
	r := New(configrules.CurlConfig{}).WithEvaluator(pe)
	return hookio.Verdict(r.Evaluate(makeInput(cmd))).Decision
}

// TestGetMode_Approves pins W1 (bead pg2-b1lir, ruling pg2-r9kv7): -G / --get turns
// --data-urlencode (and the other data flags) into query parameters of a loopback
// GET, so the Prometheus read shape that pool sessions were denied ~77 times is
// approved.
func TestGetMode_Approves(t *testing.T) {
	cmds := []string{
		`curl -sG --data-urlencode 'query=up' http://127.0.0.1:9090/api/v1/query`,
		`curl -s -G --data-urlencode 'query=rate(x[5m])' http://127.0.0.1:9090/api/v1/query`,
		`curl --get --data-urlencode query=up http://localhost:9090/api/v1/query`,
		`curl -Gs --data-urlencode query=up http://localhost:9090/api/v1/query`,
		`curl -G -d 'a=b' http://127.0.0.1:3000/api/search`,
		`curl -G --data-raw 'a=b' http://127.0.0.1:3000/api/search`,
		`curl -G --data 'a=b' --data-urlencode 'c=d e' http://metrics.localhost/x`,
		`curl -G --data-urlencode 'query=up' --url http://127.0.0.1:9090/api/v1/query`,
		`curl -sG -m 10 --data-urlencode 'query=up' http://[::1]:9090/api/v1/query`,
		`curl -sG --data-urlencode 'query=up' -o /dev/null http://127.0.0.1:9090/api/v1/query`,
		`curl -sG --data-urlencode 'query=up' -o /tmp/prom-out.json http://127.0.0.1:9090/api/v1/query`,
		`curl -sGI http://127.0.0.1:9090/-/healthy`,
		`curl -sG -X GET --data-urlencode 'query=up' http://127.0.0.1:9090/api/v1/query`,
		// a name containing '@' is literal content once an '=' is present.
		`curl -sG --data-urlencode 'email=a@b' http://127.0.0.1:9090/q`,
		`curl -sG --data-urlencode 'query=up' http://127.0.0.1:9090/api/v1/query | jq .`,
	}
	for _, cmd := range cmds {
		if got := decide(t, cmd); got != hookio.Approve {
			t.Errorf("%q: got %s, want approve", cmd, got)
		}
	}
}

// TestGetMode_StaysDenied pins the ruling's invariants: still loopback or
// .localhost only, and still no -X non-GET, -T, -F, -K, or -o outside the write
// roots; plus the shapes that would change where the request goes.
func TestGetMode_StaysDenied(t *testing.T) {
	cmds := map[string]string{
		// host
		`curl -sG --data-urlencode q=up https://evil.example/`:                  "non-loopback host",
		`curl -sG --data-urlencode q=up https://api.github.com/x`:               "a legacy read host is NOT loopback",
		`curl -sG --data-urlencode q=up http://127.0.0.1@evil.example/x`:        "userinfo trick, host is evil.example",
		`curl -sG --data-urlencode q=up http://127.0.0.1/ http://evil.example/`: "second URL on a foreign host",
		`curl -sG --data-urlencode q=up 127.0.0.1:9090/x`:                       "schemeless positional is not a proven loopback URL",
		// method
		`curl -sG -X POST --data-urlencode q=up http://127.0.0.1:9090/x`:  "explicit POST",
		`curl -sG -XDELETE --data-urlencode q=up http://127.0.0.1:9090/x`: "explicit DELETE",
		`curl -sG --request=PUT --data q=up http://127.0.0.1:9090/x`:      "explicit PUT",
		// bodies -G does not convert
		`curl -sG -T /etc/hosts http://127.0.0.1:9090/x`:            "-T upload",
		`curl -sG --upload-file /etc/hosts http://127.0.0.1:9090/x`: "--upload-file",
		`curl -sG -F a=b http://127.0.0.1:9090/x`:                   "-F form",
		`curl -sG --form a=@/etc/hosts http://127.0.0.1:9090/x`:     "--form",
		`curl -sG --json '{}' http://127.0.0.1:9090/x`:              "--json",
		// config file
		`curl -sG -K /tmp/curlrc http://127.0.0.1:9090/x`:       "-K",
		`curl -sGK /tmp/curlrc http://127.0.0.1:9090/x`:         "glued -K in a cluster",
		`curl -sG --config /tmp/curlrc http://127.0.0.1:9090/x`: "--config",
		// -o outside the write roots, and other file writes
		`curl -sG --data-urlencode q=up -o /etc/hosts http://127.0.0.1:9090/x`: "-o outside write roots",
		`curl -sG -o/etc/hosts http://127.0.0.1:9090/x`:                        "glued -o outside write roots",
		`curl -sG --output /etc/hosts http://127.0.0.1:9090/x`:                 "--output outside write roots",
		`curl -sG -c /etc/jar http://127.0.0.1:9090/x`:                         "cookie jar write",
		`curl -sG -D /etc/hdr http://127.0.0.1:9090/x`:                         "header dump write",
		`curl -sG --trace /etc/tr http://127.0.0.1:9090/x`:                     "trace write",
		// local files read into the request
		`curl -sG --data-urlencode @/etc/passwd http://127.0.0.1:9090/x`:  "urlencode @file",
		`curl -sG --data-urlencode q@/etc/passwd http://127.0.0.1:9090/x`: "urlencode name@file",
		`curl -sG -d @/etc/passwd http://127.0.0.1:9090/x`:                "-d @file",
		`curl -sG --data-binary @/etc/passwd http://127.0.0.1:9090/x`:     "--data-binary @file",
		`curl -sG -H @/etc/passwd http://127.0.0.1:9090/x`:                "header from file",
		// where the request goes / who answers
		`curl -sG -L --data-urlencode q=up http://127.0.0.1:9090/x`:                    "follow redirects off the loopback host",
		`curl -sG --unix-socket /var/run/docker.sock http://localhost/containers/json`: "unix socket reaches docker.sock",
		`curl -sG -x http://evil.example:3128 http://127.0.0.1:9090/x`:                 "proxy",
		`curl -sG --resolve 127.0.0.1:9090:6.6.6.6 http://127.0.0.1:9090/x`:            "resolve rewrites the host",
		`curl -sG --connect-to ::evil.example: http://127.0.0.1:9090/x`:                "connect-to rewrites the host",
		// parser safety: an unknown flag, --next and a value that looks like -G
		`curl -sG --not-a-curl-flag http://127.0.0.1:9090/x`:                             "unknown long flag fails closed",
		`curl -sG http://127.0.0.1:9090/x --next -X POST -d a=b http://127.0.0.1:9090/y`: "--next restarts option parsing",
		`curl -H -G --data-urlencode q=up http://127.0.0.1:9090/x`:                       "-G is only the VALUE of -H: this is a POST",
		`curl -d x -H -G http://127.0.0.1:9090/x`:                                        "-G is only the VALUE of -H: this is a POST",
		`curl -A --get -d x http://127.0.0.1:9090/x`:                                     "--get is only the VALUE of -A: this is a POST",
		`curl -sG --data-urlencode`:                                                      "missing operand",
	}
	for cmd, why := range cmds {
		// Get mode itself must never approve any of these.
		leaf := leafArgs(t, cmd)
		inv, ok := parseInvocation(leaf)
		pe := patheval.NewWithCWD("/tmp", "/tmp")
		r := New(configrules.CurlConfig{}).WithEvaluator(pe)
		if ok && r.getModeApproves(inv, "/tmp") && r.getModeOutputsOK(inv, "/tmp") {
			t.Errorf("%q: get mode approves it, want refusal (%s)", cmd, why)
		}
		// And the whole rule must not approve it, except for the shapes the LEGACY
		// scan has always approved (no body flag, so the legacy scan reads a plain
		// GET to a loopback host): see legacyApprovesRegardless.
		if legacyApprovesRegardless[cmd] {
			continue
		}
		if got := decide(t, cmd); got == hookio.Approve {
			t.Errorf("%q: the rule approves it, want not approved (%s)", cmd, why)
		}
	}
}

// legacyApprovesRegardless lists get-mode refusals that the pre-existing legacy
// scan still approves, because they carry no body flag and the legacy scan only
// judges the host and method of a plain GET. They are NOT widened by W1 (get mode
// refuses them); they are the findings recorded in the bead pg2-b1lir report:
// redirecting or writing options the legacy scan never inspected. -K and -o
// outside the write roots are NOT in this list: Evaluate refuses those rule-wide.
var legacyApprovesRegardless = map[string]bool{
	`curl -sG --resolve 127.0.0.1:9090:6.6.6.6 http://127.0.0.1:9090/x`:            true,
	`curl -sG --connect-to ::evil.example: http://127.0.0.1:9090/x`:                true,
	`curl -sG -c /etc/jar http://127.0.0.1:9090/x`:                                 true,
	`curl -sG -D /etc/hdr http://127.0.0.1:9090/x`:                                 true,
	`curl -sG --trace /etc/tr http://127.0.0.1:9090/x`:                             true,
	`curl -sG --unix-socket /var/run/docker.sock http://localhost/containers/json`: true,
	`curl -sG --not-a-curl-flag http://127.0.0.1:9090/x`:                           true,
	`curl -sG -H @/etc/passwd http://127.0.0.1:9090/x`:                             true,
}

// leafArgs returns the argv (after `curl`) of the first leaf of cmd.
func leafArgs(t *testing.T, cmd string) []string {
	t.Helper()
	leaves, err := hookio.LeavesOf(makeInput(cmd))
	if err != nil || len(leaves) == 0 {
		t.Fatalf("%q: no leaves: %v", cmd, err)
	}
	return leaves[0].Args
}

// TestGetMode_NoEvaluatorAllowsOnlyDevNull proves the legacy construction (no
// evaluator) never guesses a write zone: -o /dev/null and -o - are the only
// outputs get mode approves.
func TestGetMode_NoEvaluatorAllowsOnlyDevNull(t *testing.T) {
	r := New(configrules.CurlConfig{})
	want := map[string]bool{
		`curl -sG -o /dev/null http://127.0.0.1:9090/x`:     true,
		`curl -sG -o - http://127.0.0.1:9090/x`:             true,
		`curl -sG -o /tmp/out.json http://127.0.0.1:9090/x`: false,
		`curl -sGO http://127.0.0.1:9090/x`:                 false,
	}
	for cmd, approved := range want {
		inv, ok := parseInvocation(leafArgs(t, cmd))
		got := ok && r.getModeApproves(inv, "/tmp") && r.getModeOutputsOK(inv, "/tmp")
		if got != approved {
			t.Errorf("%q: get mode approves = %v, want %v", cmd, got, approved)
		}
	}
}

// TestCurl_ConfigAndOutputAreRefusedRuleWide pins the two invariants the W1 ruling
// names ("still denies -K and -o outside write roots"), which the legacy scan never
// checked: they now hold for every invocation the strict walk understands.
func TestCurl_ConfigAndOutputAreRefusedRuleWide(t *testing.T) {
	want := map[string]hookio.Decision{
		`curl -K /tmp/curlrc http://127.0.0.1:9090/x`:           hookio.NoOpinion,
		`curl --config=/tmp/curlrc http://127.0.0.1:9090/x`:     hookio.NoOpinion,
		`curl -sK/tmp/curlrc http://127.0.0.1:9090/x`:           hookio.NoOpinion,
		`curl -o /etc/hosts http://127.0.0.1:9090/x`:            hookio.NoOpinion,
		`curl --output /etc/hosts http://127.0.0.1:9090/x`:      hookio.NoOpinion,
		`curl -s -o /dev/null http://127.0.0.1:9090/x`:          hookio.Approve,
		`curl -s -o /tmp/prom-out.json http://127.0.0.1:9090/x`: hookio.Approve,
	}
	for cmd, d := range want {
		if got := decide(t, cmd); got != d {
			t.Errorf("%q: got %s, want %s", cmd, got, d)
		}
	}
}

// TestGetMode_IsAdditive proves the legacy verdicts did not move: everything the
// legacy scan approved or refused for a non-G command is unchanged.
func TestGetMode_IsAdditive(t *testing.T) {
	want := map[string]hookio.Decision{
		`curl https://api.github.com/repos/foo/bar`:              hookio.Approve,
		`curl -sL https://api.github.com/repos/foo/bar`:          hookio.Approve,
		`curl -d x http://127.0.0.1:9090/x`:                      hookio.NoOpinion,
		`curl --data-urlencode q=up http://127.0.0.1:9090/x`:     hookio.NoOpinion,
		`curl -X POST http://127.0.0.1:9090/x`:                   hookio.NoOpinion,
		`curl -G https://evil.example/`:                          hookio.NoOpinion,
		`curl -sG --data-urlencode q=up https://api.github.com/`: hookio.NoOpinion,
	}
	for cmd, d := range want {
		if got := decide(t, cmd); got != d {
			t.Errorf("%q: got %s, want %s", cmd, got, d)
		}
	}
}

// TestCurlFlagTables_Disjoint guards the generated tables: a flag is value-taking
// or boolean, never both.
func TestCurlFlagTables_Disjoint(t *testing.T) {
	for name := range curlLongValueFlags {
		if curlLongBoolFlags[name] {
			t.Errorf("%s is in both the value and boolean long-flag tables", name)
		}
	}
	for _, c := range curlShortValueFlags {
		for _, b := range curlShortBoolFlags {
			if c == b {
				t.Errorf("-%c is in both the short value and boolean tables", c)
			}
		}
	}
}

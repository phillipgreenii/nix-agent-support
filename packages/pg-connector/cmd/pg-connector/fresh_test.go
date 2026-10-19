package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --fresh on the read verbs: the flag exists on pr files, pr commits, pr review
// pending, pr list and ci list (and pr show / issue show keep theirs), and a
// set flag is forwarded as the request argument fresh: true to a backend that
// declares cache_opt_out ONLY. Every request to any other backend, and every
// request made without the flag, is byte-identical to what it was before.

// freshFakeBackend is a fake backend that records every request it receives, one
// JSON line per call, and answers every read op with a fixed, well-formed
// result. Its capabilities declare cache_opt_out when optOut is set. With
// notFound set, every id-keyed read answers not_found (so a try-each dispatch
// moves on to the next registered backend).
type freshFakeBackend struct {
	dir  string
	name string
}

func newFreshFakeBackend(t *testing.T, name string, optOut, notFound bool) *freshFakeBackend {
	t.Helper()
	dir := t.TempDir()
	caps := `{"protocolVersion":1,"schemaVersions":{"pr":1,"ci":1,"issue":1},"ops":["capabilities","show","list","files","commits","review_pending","list_runs"]}`
	if optOut {
		caps = `{"protocolVersion":1,"schemaVersions":{"pr":1,"ci":1,"issue":1},"ops":["capabilities","show","list","files","commits","review_pending","list_runs"],"vocabulary":{"cache_opt_out":true}}`
	}
	idKeyed := func(result string) string {
		if notFound {
			return `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"gone"}}`
		}
		return `{"protocolVersion":1,"schemaVersion":1,"result":` + result + `}`
	}
	answers := map[string]string{
		"show":           idKeyed(`{"id":"pr-1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false,"as_of":"AS_OF"}`),
		"files":          idKeyed(`{"id":"pr-1","files":[{"path":"a.go","additions":1,"deletions":0}]}`),
		"commits":        idKeyed(`{"id":"pr-1","commits":[{"sha":"abc","author":"a","message":"m"}]}`),
		"review_pending": idKeyed(`{"pending":false,"head_sha":"h1","as_of":"2026-01-01T00:00:00Z"}`),
		"list":           `{"protocolVersion":1,"schemaVersion":1,"result":{"entities":[{"id":"o/r#1","repo":"o/r","number":1,"title":"t","state":"open","branch":"b","base":"main","author":"a","url":"u","draft":false,"merged":false}],"present_ids":["o/r#1"],"cursor":null,"truncated":false}}`,
		"list_runs":      `{"protocolVersion":1,"schemaVersion":1,"result":[{"id":"run-1","name":"build","status":"completed","conclusion":"success","url":"u","provider":"p","head_sha":"deadbeef","pr_id":"pr-1"}]}`,
	}
	asOf := time.Now().UTC().Format(time.RFC3339)
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\nreq=$(cat)\nprintf '%s\\n' \"$req\" >> '" + filepath.Join(dir, "requests.log") + "'\n")
	sb.WriteString("case \"$req\" in\n")
	fmt.Fprintf(&sb, "*'\"op\":\"capabilities\"'*) cat <<'FAKE_EOF'\n%s\nFAKE_EOF\n;;\n", caps)
	for op, ans := range answers {
		// AS_OF is the fixture's creation time: recent enough to sit inside the
		// umbrella's read-through window, and constant across calls so two runs
		// of one verb compare equal.
		ans = strings.ReplaceAll(ans, "AS_OF", asOf)
		fmt.Fprintf(&sb, "*'\"op\":\"%s\"'*) cat <<'FAKE_EOF'\n%s\nFAKE_EOF\n;;\n", op, ans)
	}
	sb.WriteString("esac\n")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("write fake backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &freshFakeBackend{dir: dir, name: name}
}

// argsOf returns the args object of every recorded request of op, byte for
// byte as the umbrella sent it, in call order.
func (b *freshFakeBackend) argsOf(t *testing.T, op string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b.dir, "requests.log"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var req struct {
			Op   string          `json:"op"`
			Args json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("recorded request %q is not JSON: %v", line, err)
		}
		if req.Op == op {
			out = append(out, string(req.Args))
		}
	}
	return out
}

// writeFreshConfig registers backends, in order, under connector.pr,
// connector.ci and connector.issue.
func writeFreshConfig(t *testing.T, backends ...*freshFakeBackend) {
	t.Helper()
	dir := t.TempDir()
	var sb strings.Builder
	sb.WriteString("connector:\n")
	for _, typ := range []string{"pr", "ci", "issue"} {
		sb.WriteString("  " + typ + ":\n")
		for _, b := range backends {
			sb.WriteString("    - " + b.name + "\n")
		}
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
	t.Setenv("XDG_STATE_HOME", dir)
}

// freshVerb is one verb under test: its command line, the daemon op it sends,
// and the args object it sends without and with fresh for a backend that
// declares cache_opt_out.
type freshVerb struct {
	name      string
	cmd       []string
	op        string
	plain     string
	withFresh string
	// idKeyed verbs try registered backends in turn (a not_found answer moves
	// on to the next); the others fan out.
	idKeyed bool
}

var freshVerbs = []freshVerb{
	{name: "pr show", cmd: []string{"pr", "show", "pr-1"}, op: "show", plain: `{"id":"pr-1"}`, withFresh: `{"fresh":true,"id":"pr-1"}`, idKeyed: true},
	{name: "issue show", cmd: []string{"issue", "show", "pr-1"}, op: "show", plain: `{"id":"pr-1"}`, withFresh: `{"fresh":true,"id":"pr-1"}`, idKeyed: true},
	{name: "pr files", cmd: []string{"pr", "files", "pr-1"}, op: "files", plain: `{"id":"pr-1"}`, withFresh: `{"fresh":true,"id":"pr-1"}`, idKeyed: true},
	{name: "pr commits", cmd: []string{"pr", "commits", "pr-1"}, op: "commits", plain: `{"id":"pr-1"}`, withFresh: `{"fresh":true,"id":"pr-1"}`, idKeyed: true},
	{name: "pr review pending", cmd: []string{"pr", "review", "pending", "pr-1"}, op: "review_pending", plain: `{"id":"pr-1"}`, withFresh: `{"fresh":true,"id":"pr-1"}`, idKeyed: true},
	{name: "pr list", cmd: []string{"pr", "list", "--query", "mine"}, op: "list", plain: `{"cursor":null,"ids_only":false,"query":"mine"}`, withFresh: `{"cursor":null,"fresh":true,"ids_only":false,"query":"mine"}`},
	{name: "ci list", cmd: []string{"ci", "list", "pr-1"}, op: "list_runs", plain: `{"pr_id":"pr-1"}`, withFresh: `{"fresh":true,"pr_id":"pr-1"}`},
}

func withFreshFlag(cmd []string) []string {
	return append(append([]string{}, cmd...), "--fresh")
}

func TestFresh_ForwardedOnlyWhenSetAndOnlyToACacheOptOutBackend(t *testing.T) {
	for _, v := range freshVerbs {
		for _, optOut := range []bool{true, false} {
			for _, fresh := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/optOut=%t/fresh=%t", v.name, optOut, fresh), func(t *testing.T) {
					b := newFreshFakeBackend(t, "fresh-single", optOut, false)
					writeFreshConfig(t, b)
					cmd := v.cmd
					want := v.plain
					if fresh {
						cmd = withFreshFlag(cmd)
						if optOut {
							want = v.withFresh
						}
					}
					if stdout, code := freshRun(t, cmd); code != 0 {
						t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
					}
					got := b.argsOf(t, v.op)
					if len(got) != 1 || got[0] != want {
						t.Fatalf("%s requests = %v, want exactly one with args %s", v.op, got, want)
					}
				})
			}
		}
	}
}

func freshRun(t *testing.T, cmd []string) (string, int) {
	t.Helper()
	stdout, _, code := executePr(t, cmd)
	return stdout, code
}

func TestFresh_FanOutGivesOnlyTheOptedOutBackendTheKey(t *testing.T) {
	for _, v := range freshVerbs {
		if v.idKeyed {
			continue
		}
		for _, optedOutFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/optedOutFirst=%t", v.name, optedOutFirst), func(t *testing.T) {
				optedOut := newFreshFakeBackend(t, "fresh-fan-optout", true, false)
				plain := newFreshFakeBackend(t, "fresh-fan-plain", false, false)
				if optedOutFirst {
					writeFreshConfig(t, optedOut, plain)
				} else {
					writeFreshConfig(t, plain, optedOut)
				}
				if stdout, code := freshRun(t, withFreshFlag(v.cmd)); code != 0 {
					t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
				}
				if got := optedOut.argsOf(t, v.op); len(got) != 1 || got[0] != v.withFresh {
					t.Fatalf("opted-out backend %s requests = %v, want one with args %s", v.op, got, v.withFresh)
				}
				if got := plain.argsOf(t, v.op); len(got) != 1 || got[0] != v.plain {
					t.Fatalf("plain backend %s requests = %v, want one with args %s (no fresh key)", v.op, got, v.plain)
				}
			})
		}
	}
}

func TestFresh_TargetedTryEachDecidesPerBackend(t *testing.T) {
	for _, v := range freshVerbs {
		if !v.idKeyed {
			continue
		}
		for _, optedOutFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/optedOutFirst=%t", v.name, optedOutFirst), func(t *testing.T) {
				// The first registered backend does not know the id, so the
				// dispatch goes on to the second; each is asked once.
				optedOut := newFreshFakeBackend(t, "fresh-try-optout", true, optedOutFirst)
				plain := newFreshFakeBackend(t, "fresh-try-plain", false, !optedOutFirst)
				if optedOutFirst {
					writeFreshConfig(t, optedOut, plain)
				} else {
					writeFreshConfig(t, plain, optedOut)
				}
				if stdout, code := freshRun(t, withFreshFlag(v.cmd)); code != 0 {
					t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
				}
				if got := optedOut.argsOf(t, v.op); len(got) != 1 || got[0] != v.withFresh {
					t.Fatalf("opted-out backend %s requests = %v, want one with args %s", v.op, got, v.withFresh)
				}
				if got := plain.argsOf(t, v.op); len(got) != 1 || got[0] != v.plain {
					t.Fatalf("plain backend %s requests = %v, want one with args %s (no fresh key)", v.op, got, v.plain)
				}
			})
		}
	}
}

func TestFresh_UnsetFlagMakesNoCapabilitiesCallOnTheNewReadVerbs(t *testing.T) {
	for _, v := range freshVerbs {
		if v.op == "show" || v.name == "ci list" {
			// show and ci list ask capabilities for the umbrella cache gate
			// regardless of the flag.
			continue
		}
		t.Run(v.name, func(t *testing.T) {
			b := newFreshFakeBackend(t, "fresh-nocaps", true, false)
			writeFreshConfig(t, b)
			if stdout, code := freshRun(t, v.cmd); code != 0 {
				t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
			}
			if got := b.argsOf(t, "capabilities"); len(got) != 0 {
				t.Fatalf("capabilities requests without --fresh = %v, want none", got)
			}
		})
	}
}

func TestFresh_HelpListsTheFlagOnEveryReadVerb(t *testing.T) {
	for _, v := range freshVerbs {
		t.Run(v.name, func(t *testing.T) {
			stdout, _, code := executePr(t, append(append([]string{}, v.cmd[:len(v.cmd):len(v.cmd)]...), "--help"))
			if code != 0 || !strings.Contains(stdout, "--fresh") {
				t.Fatalf("--help exit %d, output does not list --fresh:\n%s", code, stdout)
			}
		})
	}
}

func TestFresh_HelpSaysItIsForReadingBackOwnWritesNotPolling(t *testing.T) {
	stdout, _, _ := executePr(t, []string{"pr", "files", "--help"})
	for _, want := range []string{"out-of-band write", "polling paths SHOULD NOT"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("pr files --help does not say %q:\n%s", want, stdout)
		}
	}
}

func TestFresh_ChangesNoOutputAndNoExitCode(t *testing.T) {
	for _, v := range freshVerbs {
		t.Run(v.name, func(t *testing.T) {
			b := newFreshFakeBackend(t, "fresh-same-output", true, false)
			writeFreshConfig(t, b)
			without, codeWithout := freshRun(t, v.cmd)
			with, codeWith := freshRun(t, withFreshFlag(v.cmd))
			if codeWith != codeWithout || with != without {
				t.Fatalf("--fresh changed the result: exit %d vs %d\nwith:    %s\nwithout: %s", codeWith, codeWithout, with, without)
			}
		})
	}
}

func TestFresh_ExitCodeUnchangedWhenTheBackendAnswersNotFound(t *testing.T) {
	for _, v := range freshVerbs {
		if !v.idKeyed {
			continue
		}
		t.Run(v.name, func(t *testing.T) {
			b := newFreshFakeBackend(t, "fresh-notfound", true, true)
			writeFreshConfig(t, b)
			without, codeWithout := freshRun(t, v.cmd)
			with, codeWith := freshRun(t, withFreshFlag(v.cmd))
			if codeWithout != 4 || codeWith != codeWithout || with != without {
				t.Fatalf("not_found exit %d (without) vs %d (with --fresh), want 4 for both and equal output\nwith:    %s\nwithout: %s", codeWithout, codeWith, with, without)
			}
		})
	}
}

func TestFresh_ShowStillBypassesTheUmbrellaReadThroughForABackendThatDoesNotOptOut(t *testing.T) {
	b := newFreshFakeBackend(t, "fresh-readthrough", false, false)
	writeFreshConfig(t, b)
	for i, cmd := range [][]string{{"pr", "show", "pr-1"}, {"pr", "show", "pr-1"}, {"pr", "show", "pr-1", "--fresh"}} {
		if stdout, code := freshRun(t, cmd); code != 0 {
			t.Fatalf("call %d: exit code = %d; stdout=%s", i, code, stdout)
		}
	}
	// The second read is served from the umbrella cache; --fresh goes to the
	// origin again, and the request carries no fresh key.
	got := b.argsOf(t, "show")
	if len(got) != 2 || got[0] != `{"id":"pr-1"}` || got[1] != `{"id":"pr-1"}` {
		t.Fatalf("show requests = %v, want two plain {id} requests (the middle read answered from the cache)", got)
	}
}

package cmdparse

import (
	"reflect"
	"strings"
	"testing"
)

// Fuzz harnesses over the four parser primitives that every Bash decision hinges
// on: Parse, splitCompound, tokenize, and EnumerateSubstitutions. The engine
// splits a compound into leaves (Parse/splitCompound), tokenizes each leaf
// (tokenize), and recurses every substitution body (EnumerateSubstitutions), then
// folds the leaves most-restrictive-wins. If ANY of these silently loses a
// command — a leaf that never becomes its own evaluated leaf, a compound operator
// that stays glued inside one segment, a substitution body that is never
// enumerated — a dangerous command escapes evaluation and is green-lit as part of
// an otherwise-safe expression (the pg2-t4uyx / pg2-1q5i3 bypass class).
//
// These harnesses assert the invariants that must hold for ALL inputs (never
// panic; deterministic; and — the security linchpin — a parsed leaf/segment is
// atomic: re-splitting it never reveals a further top-level command it was hiding).
// Run as ordinary tests (seed corpus only) under `go test ./...`; run as real
// fuzzers with e.g. `go test -run '^$' -fuzz FuzzParse -fuzztime 30s ./internal/cmdparse`.

// fuzzSeeds are the shared corpus for every harness: representative shell forms
// plus the known bypass strings (pg2-t4uyx compound/ampersand/wrapper/dynamic-arg
// and pg2-1q5i3 substitution-recursion classes) and adversarial malformed inputs
// (unterminated quotes/parens/substitutions, deep nesting) that historically
// desynced the byte scanner.
var fuzzSeeds = []string{
	// --- representative, well-formed shell ---
	"git status",
	`git commit -m "hello world"`,
	"cd /tmp && ls -la",
	"echo a; echo b",
	"cat file | grep foo",
	"FOO=bar git status",
	"diff <(sort a) <(sort b)",
	"tee >(wc -l) > /tmp/out",
	"echo $(git rev-parse HEAD)",
	"for f in *.md; do echo \"$f\"; cat \"$f\"; done",
	"while read line; do echo \"$line\"; done",
	"(cd /tmp && ls -la)",
	`find /tmp -type f \( -name "*.nix" \) 2>/dev/null`,
	"cat <<EOF\nhello\nEOF",
	"nix build .#myPackage # build the package",
	// --- the 8 known bypasses (each an excellent adversarial seed) ---
	"git status && rm -rf ~/important",         // 1: compound short-circuit
	"echo hi & rm -rf ~/important",             // 2: bare & separator
	"nix develop -c rm -rf /etc",               // 3: nix develop -c
	`git -c core.pager="touch /tmp/pwned" log`, // 4: git -c injection
	"rm -rf $HOME/.ssh",                        // 5: $VAR path
	"cp secret $HOME/exfil",                    // 5: $VAR path (write+dynamic)
	"echo $(rm -rf ~/x)",                       // 6: recurse substitution body
	"$(cat <(rm -rf ~))",                       // 6: process sub nested in cmd sub
	"echo `rm -rf ~/x`",                        // 6: backtick substitution
	"(echo pwned) > /etc/passwd",               // subshell trailing redirect
	"env rm -rf /etc",                          // env wrapper prefix
	"command rm -rf /etc",                      // command wrapper prefix
	"export LD_PRELOAD=/evil.so && git status", // env-injector guard
	// --- adversarial / malformed (best-effort safe-default paths) ---
	"echo $(oops",                // unterminated cmd sub
	"echo `unterminated",         // unterminated backtick
	"echo '",                     // unterminated single quote
	`echo "`,                     // unterminated double quote
	"(a; b",                      // unterminated subshell
	"$(cat $(cat $(malicious)))", // deep nesting
	"echo $((1+2)) $(id)",        // arithmetic then cmd sub
	"echo '$(rm -rf ~)'",         // literal single-quoted substitution
	`echo "\$(rm -rf ~)"`,        // escaped-dollar literal
	"",                           // empty
	"   ",                        // whitespace only
	"#",                          // bare comment marker
	"& & &",                      // only separators
	";;;",                        // only separators
	// Fuzz-found leaf-drop bypass: `#` right after `;`/`&` (no space) is a bash
	// comment; an unterminated quote in it must NOT swallow the newline and drop
	// the next line's command (pg2-t4uyx class; fixed in splitCompound).
	"echo hi;#\"x\nrm -rf /etc",
	"git status &#\"y\nrm -rf ~",
}

// nonBlankSegments counts the segments that carry a non-whitespace command; a
// blank/whitespace segment is dropped by Parse and does not represent a command.
func nonBlankSegments(segs []string) int {
	n := 0
	for _, s := range segs {
		if strings.TrimSpace(s) != "" {
			n++
		}
	}
	return n
}

// FuzzParse fuzzes Parse — the top-level leaf splitter every rule consults. It
// asserts Parse never panics, is deterministic, and (the security invariant) that
// every returned leaf is ATOMIC: re-splitting its Raw never yields more than one
// command-bearing segment, and re-parsing its Raw reproduces the same executable.
// A leaf that hides a further top-level command, or that re-parses to a different
// executable than the engine believes it holds, is exactly how a dangerous command
// escapes the most-restrictive fold (pg2-t4uyx).
func FuzzParse(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		leaves := Parse(cmd)

		// Determinism: the fold must not depend on scan order/aliasing.
		if again := Parse(cmd); !reflect.DeepEqual(leaves, again) {
			t.Fatalf("Parse(%q) is non-deterministic:\n first=%#v\n again=%#v", cmd, leaves, again)
		}

		for _, leaf := range leaves {
			// Atomicity: a single parsed leaf must not conceal a further top-level
			// separator (;, &&, ||, |, bare &, subshell) — otherwise the hidden
			// second command is never evaluated as its own leaf.
			if n := nonBlankSegments(splitCompound(leaf.Raw)); n > 1 {
				t.Fatalf("Parse(%q): leaf Raw %q re-splits into %d segments; a compound is hiding inside one leaf (escapes evaluation)", cmd, leaf.Raw, n)
			}
			if leaf.Executable == "" {
				continue // command-less (redirection/heredoc-only) leaf — no exec to re-check
			}
			// Idempotence: the engine re-feeds leaf.Raw as a synthetic command
			// (mustBashJSON(pc.Raw)); re-parsing it MUST reproduce the same
			// executable, or a rule would judge a different command than was folded.
			reparsed := Parse(leaf.Raw)
			if len(reparsed) == 0 {
				t.Fatalf("Parse(%q): leaf %q (exec %q) re-parses to zero leaves; the executable is dropped on re-feed", cmd, leaf.Raw, leaf.Executable)
			}
			if reparsed[0].Executable != leaf.Executable {
				t.Fatalf("Parse(%q): leaf Raw %q re-parses to exec %q, want %q (non-idempotent executable)", cmd, leaf.Raw, reparsed[0].Executable, leaf.Executable)
			}
		}
	})
}

// FuzzSplitCompound fuzzes splitCompound — the quote/paren-aware separator scanner
// under Parse and IsSafeSubstitutionBody. It asserts no panic, determinism, and
// segment atomicity: re-splitting any produced segment yields at most one
// command-bearing segment, proving the split is COMPLETE (a leftover top-level
// separator inside a segment is a command that would silently ride along).
func FuzzSplitCompound(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		segs := splitCompound(cmd)
		if again := splitCompound(cmd); !reflect.DeepEqual(segs, again) {
			t.Fatalf("splitCompound(%q) is non-deterministic:\n first=%#v\n again=%#v", cmd, segs, again)
		}
		for _, seg := range segs {
			if n := nonBlankSegments(splitCompound(seg)); n > 1 {
				t.Fatalf("splitCompound(%q): segment %q re-splits into %d segments; splitting is incomplete (a separator escaped)", cmd, seg, n)
			}
		}
	})
}

// FuzzTokenize fuzzes tokenize — the per-leaf token/process-substitution scanner.
// It asserts no panic, determinism, and that every extracted process-substitution
// body is a real substring of the leaf (a slice-math error would fabricate text or
// panic) that itself tokenizes without panicking (the engine recurses procSubs).
func FuzzTokenize(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seg string) {
		tokens, procSubs := tokenize(seg)
		tokens2, procSubs2 := tokenize(seg)
		if !reflect.DeepEqual(tokens, tokens2) || !reflect.DeepEqual(procSubs, procSubs2) {
			t.Fatalf("tokenize(%q) is non-deterministic", seg)
		}
		for _, ps := range procSubs {
			if !strings.Contains(seg, ps) {
				t.Fatalf("tokenize(%q): process-sub body %q is not a substring of the input (slice-math bug)", seg, ps)
			}
			tokenize(ps) // must not panic on the recursed body
		}
	})
}

// FuzzEnumerateSubstitutions fuzzes EnumerateSubstitutions — the shared enumerator
// the engine uses to recurse every $(...) / `...` / <(...) / >(...) body
// (pg2-1q5i3). It asserts no panic, determinism, that each returned body is a real
// substring of the input, that Kind and IsCommandSubstitution stay consistent, and
// that feeding every body back through the enumerator and the static allowlist
// floor (the exact re-entrant path the engine takes) never panics.
func FuzzEnumerateSubstitutions(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		subs := EnumerateSubstitutions(s)
		if again := EnumerateSubstitutions(s); !reflect.DeepEqual(subs, again) {
			t.Fatalf("EnumerateSubstitutions(%q) is non-deterministic", s)
		}
		for _, sub := range subs {
			if !strings.Contains(s, sub.Body) {
				t.Fatalf("EnumerateSubstitutions(%q): body %q is not a substring of the input (slice-math bug)", s, sub.Body)
			}
			wantCmd := sub.Kind == SubstCommand || sub.Kind == SubstBacktick
			if sub.IsCommandSubstitution() != wantCmd {
				t.Fatalf("EnumerateSubstitutions(%q): body %q Kind=%d IsCommandSubstitution=%v, want %v", s, sub.Body, sub.Kind, sub.IsCommandSubstitution(), wantCmd)
			}
			// The engine re-enumerates and consults the static floor on each body;
			// neither may panic on a fuzzed body.
			EnumerateSubstitutions(sub.Body)
			IsSafeSubstitutionBody(sub.Body)
			HasUnsafeCommandSubstitution(sub.Body)
		}
	})
}

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Tests of the focus group's operator-facing help and its shell completions:
// the required phrases of `focus --help` and `focus select --help`, the
// completion of the keys `explain` and `pull` take from the store's
// candidates (quiet and read-only), and that the completion script of the
// whole command still generates.

func mustHaveAll(t *testing.T, what, text string, phrases ...string) {
	t.Helper()
	for _, p := range phrases {
		if !strings.Contains(text, p) {
			t.Errorf("%s lacks %q:\n%s", what, p, text)
		}
	}
}

func TestFocusSelectHelpDocumentsReplyGrammarExitCodesAndDryRun(t *testing.T) {
	out, _, err := runFocusCmd(t, newFocusSelectCmd, "--help")
	mustOK(t, err)
	// The reply grammar: every token with its meaning, and examples.
	mustHaveAll(t, "select --help", out, "Reply grammar", "ok", "-<key>", "+<key>", "cap=N")
	mustHaveAll(t, "select --help examples", out, "Examples:", "| pg-desk focus select --dry-run", "| pg-desk focus select --apply", "cap=4")
	// The exit codes, one line each: 0 1 2 3 6 and no other.
	for _, code := range []string{"0", "1", "2", "3", "6"} {
		if !regexp.MustCompile(`(?m)^  ` + code + `  \S`).MatchString(out) {
			t.Errorf("select --help has no exit-code line for %s:\n%s", code, out)
		}
	}
	for _, code := range []string{"4", "5", "7"} {
		if regexp.MustCompile(`(?m)^  ` + code + `  \S`).MatchString(out) {
			t.Errorf("select --help documents the unused exit code %s:\n%s", code, out)
		}
	}
	mustHaveAll(t, "select --help", out, "--dry-run", "--apply")
}

func TestFocusGroupHelpLinksVerbsAndNamesThePlanableLabel(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"focus", "--help"})
	t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) })
	mustOK(t, rootCmd.ExecuteContext(context.Background()))
	text := out.String()
	mustHaveAll(t, "focus --help", text,
		"show", "replan", "select", "pull", "close", "explain",
		"pg-focus-planable", "bd update <id> --add-label pg-focus-planable",
		"only bead marker")
	// operator_identities is described as Jira-only wherever the help
	// mentions it.
	if !strings.Contains(text, "focus.operator_identities") || !strings.Contains(text, "Jira-only key") {
		t.Errorf("focus --help must describe focus.operator_identities as a Jira-only key:\n%s", text)
	}
	// Every verb the group lists is a registered subcommand, so the help
	// never links a verb that does not exist.
	for _, verb := range []string{"show", "replan", "select", "pull", "close", "explain"} {
		c, _, err := rootCmd.Find([]string{"focus", verb})
		if err != nil || c == nil || c.Name() != verb {
			t.Errorf("focus %s is not registered: %v", verb, err)
		}
	}
}

// completeArgs runs the real `__complete` command over the whole command tree
// and returns what a shell reads: stdout (one candidate per line, then the
// ":<directive>" line) and the error. The line cobra prints on stderr is a
// debug note the shell scripts discard.
func completeArgs(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errb)
	rootCmd.SetArgs(append([]string{"__complete"}, args...))
	t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) })
	err := rootCmd.ExecuteContext(context.Background())
	return out.String(), err
}

// useReadOnlySeam points the read-only store seam at the fixture, counting
// the opens, and fails the test if the completion opened it writable.
func useReadOnlySeam(t *testing.T, f *explainFixture) {
	t.Helper()
	orig := deskStoreOpenReadOnly
	t.Cleanup(func() { deskStoreOpenReadOnly = orig })
	deskStoreOpenReadOnly = func() (*store.Store, error) { return store.OpenReadOnly(f.path) }
	origRW := deskStoreOpen
	t.Cleanup(func() { deskStoreOpen = origRW })
	deskStoreOpen = func() (*store.Store, error) {
		t.Error("a completion opened the store writable")
		return nil, errors.New("writable open")
	}
}

func completionWords(out string) map[string]bool {
	words := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		words[strings.SplitN(line, "\t", 2)[0]] = true
	}
	return words
}

func TestFocusExplainCompletesKeysFromTheStoresCandidates(t *testing.T) {
	f := newExplainFixture(t)
	useReadOnlySeam(t, f)
	out, err := completeArgs(t, "focus", "explain", "")
	mustOK(t, err)
	words := completionWords(out)
	// PRs complete as their bare number; the linked issue as its key; the
	// plan row (2) is still a candidate for explain.
	for _, want := range []string{"1", "2", "ZZ-7"} {
		if !words[want] {
			t.Errorf("explain completion lacks %q:\n%s", want, out)
		}
	}
	// Not candidates: the hidden PR and the unrelated issue.
	for _, not := range []string{"3", "ZZ-5"} {
		if words[not] {
			t.Errorf("explain completion offers the non-candidate %q:\n%s", not, out)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(out), ":4") {
		t.Errorf("completion must end with the no-file-completion directive :4:\n%s", out)
	}
	// A prefix narrows; a second argument offers nothing.
	out, err = completeArgs(t, "focus", "explain", "ZZ")
	mustOK(t, err)
	if w := completionWords(out); !w["ZZ-7"] || w["1"] {
		t.Errorf("prefix ZZ: got %v", w)
	}
	out, err = completeArgs(t, "focus", "explain", "1", "")
	mustOK(t, err)
	if w := completionWords(out); len(w) != 0 {
		t.Errorf("explain takes one key; a second completes nothing, got %v", w)
	}
}

func TestFocusPullCompletesCandidatesNotYetSelected(t *testing.T) {
	f := newExplainFixture(t)
	useReadOnlySeam(t, f)
	out, err := completeArgs(t, "focus", "pull", "")
	mustOK(t, err)
	words := completionWords(out)
	if !words["1"] || !words["ZZ-7"] {
		t.Errorf("pull completion lacks an unselected candidate:\n%s", out)
	}
	if words["2"] {
		t.Errorf("pull completion offers 2, which is already in the plan:\n%s", out)
	}
	// A key already named is not offered again.
	out, err = completeArgs(t, "focus", "pull", "1", "")
	mustOK(t, err)
	if w := completionWords(out); w["1"] || !w["ZZ-7"] {
		t.Errorf("pull completion after naming 1: %v", w)
	}
	// With --top no key is valid.
	out, err = completeArgs(t, "focus", "pull", "--top", "2", "")
	mustOK(t, err)
	if w := completionWords(out); len(w) != 0 {
		t.Errorf("pull --top completes no key, got %v", w)
	}
}

func TestFocusKeyCompletionIsSilentOnAnyError(t *testing.T) {
	f := newExplainFixture(t)
	useReadOnlySeam(t, f)
	for name, arrange := range map[string]func(){
		"store cannot be opened": func() {
			deskStoreOpenReadOnly = func() (*store.Store, error) { return nil, errors.New("no store") }
		},
		"config cannot be loaded": func() {
			deskConfigLoad = func(context.Context) (*config.Config, error) { return nil, errors.New("bad config") }
		},
		"store not migrated": func() {
			old := storeAtVersion(t, "old")
			deskStoreOpenReadOnly = func() (*store.Store, error) { return store.OpenReadOnly(old) }
		},
	} {
		t.Run(name, func(t *testing.T) {
			origCfg, origRO := deskConfigLoad, deskStoreOpenReadOnly
			t.Cleanup(func() { deskConfigLoad, deskStoreOpenReadOnly = origCfg, origRO })
			arrange()
			for _, verb := range []string{"explain", "pull"} {
				out, err := completeArgs(t, "focus", verb, "")
				if err != nil {
					t.Errorf("%s: completion returned an error: %v", verb, err)
				}
				if out != ":4\n" {
					t.Errorf("%s: a failing completion must print only the directive, got %q", verb, out)
				}
			}
		})
	}
}

func TestFocusKeyCompletionWritesNothing(t *testing.T) {
	f := newExplainFixture(t)
	useReadOnlySeam(t, f)
	before := f.snapshot(t)
	for _, verb := range []string{"explain", "pull"} {
		if _, err := completeArgs(t, "focus", verb, ""); err != nil {
			t.Fatal(err)
		}
	}
	if after := f.snapshot(t); after != before {
		t.Errorf("a completion changed the store:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestCompletionScriptStillGenerates(t *testing.T) {
	// Cobra's own `completion <shell>` commands are initialized when the root
	// executes; they call exactly these generators on the root.
	for shell, gen := range map[string]func(w io.Writer) error{
		"bash": func(w io.Writer) error { return rootCmd.GenBashCompletionV2(w, true) },
		"zsh":  func(w io.Writer) error { return rootCmd.GenZshCompletion(w) },
		"fish": func(w io.Writer) error { return rootCmd.GenFishCompletion(w, true) },
	} {
		var out bytes.Buffer
		if err := gen(&out); err != nil {
			t.Fatalf("pg-desk completion %s: %v", shell, err)
		}
		if !strings.Contains(out.String(), "pg-desk") || !strings.Contains(out.String(), "__complete") {
			t.Errorf("pg-desk completion %s generated no completion script:\n%.300s", shell, out.String())
		}
	}
	// The command that exposes them is on the tree once the root has run.
	if c, _, err := rootCmd.Find([]string{"completion", "bash"}); err != nil || c == nil || c.Name() != "bash" {
		t.Errorf("pg-desk completion bash is not on the command tree: %v", err)
	}
}

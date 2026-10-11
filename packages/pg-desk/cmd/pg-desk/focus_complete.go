package main

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
)

// Shell completion of the keys the focus verbs take as arguments: `focus
// explain <key>` and `focus pull <key>...` complete from the store's
// candidates (docs/behavior/pg-desk/focus.md, "Help and completions"). A
// completion is a read: it opens the store READ-ONLY, calls no tracker and no
// network, writes nothing, and on ANY failure (config, store, an unmigrated
// store, a panic, the time limit) it prints nothing and offers no completion,
// because a shell would show every stderr byte in the operator's prompt.
//
// Telemetry: none; nothing is logged.

// focusCompleteTimeout bounds one completion so a slow or locked store never
// hangs a tab press.
const focusCompleteTimeout = 2 * time.Second

// completeFocusExplainKeys completes the single key of `focus explain`.
func completeFocusExplainKeys(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) >= 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeFocusKeys(cmd, args, toComplete, false)
}

// completeFocusPullKeys completes the keys of `focus pull`: the candidates
// not yet in the plan and not already named. With --top given no key is
// valid, so nothing is offered.
func completeFocusPullKeys(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if cmd.Flags().Changed(focusFlagTop) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeFocusKeys(cmd, args, toComplete, true)
}

// completeFocusKeys runs the store read under focusCompleteTimeout. The
// directive is always NoFileComp: a key is never a file name.
func completeFocusKeys(cmd *cobra.Command, args []string, toComplete string, skipPlanned bool) ([]string, cobra.ShellCompDirective) {
	done := make(chan []string, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- nil
			}
		}()
		done <- focusKeyCompletions(cmd, args, toComplete, skipPlanned)
	}()
	select {
	case out := <-done:
		return out, cobra.ShellCompDirectiveNoFileComp
	case <-time.After(focusCompleteTimeout):
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// focusKeyCompletions is the read itself: "<key>\t<description>" lines for
// the candidates whose typed form starts with toComplete, in the candidate
// order (pr, jira, bead, then key). A PR completes as its bare number, the
// form every key-taking verb accepts; any other key completes as its text.
func focusKeyCompletions(cmd *cobra.Command, args []string, toComplete string, skipPlanned bool) []string {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := deskConfigLoad(ctx)
	if err != nil || cfg == nil || len(cfg.Repos) == 0 {
		return nil
	}
	st, err := deskStoreOpenReadOnly()
	if err != nil {
		return nil
	}
	defer func() { _ = st.Close() }()
	in, err := focus.Load(st, cfg, cfg.Repos[0].Remote, focusNow())
	if err != nil {
		return nil
	}
	typed := map[string]bool{}
	for _, a := range args {
		typed[a] = true
	}
	var out []string
	for _, c := range focus.Candidates(in).Candidates {
		if skipPlanned && in.PlanKeys[c.Key] {
			continue
		}
		word, desc := focusCompletionWord(c)
		if typed[word] || !strings.HasPrefix(word, toComplete) {
			continue
		}
		out = append(out, word+"\t"+desc)
	}
	return out
}

// focusCompletionWord is the typed form of a candidate and its description.
func focusCompletionWord(c focus.Candidate) (word, desc string) {
	word = c.Key.ID
	if c.Key.Type == "pr" {
		if i := strings.LastIndex(word, "#"); i >= 0 {
			word = word[i+1:]
		}
	}
	desc = string(c.Kind) + " " + c.Key.ID
	if !c.Seed {
		desc += " (linked)"
	}
	return word, desc
}

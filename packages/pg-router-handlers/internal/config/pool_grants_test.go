package config

import (
	"strings"
	"testing"
)

// wantBaseAllowedTools is the handler-wide grant set operator ruling pg2-r9kv7
// (2026-10-27, bead pg2-b1lir Work 4) left UNCHANGED: "Handler-wide additions:
// none". Every widening the ruling approved is per role (the nix-ziprecruiter role
// grants) or in CETA, never here, because this one list reaches every dontAsk pool
// role. A change to baseAllowedTools MUST be a deliberate edit of this golden, in
// the same change as the ruling that justifies it.
const wantBaseAllowedTools = "Read,Edit,Write,Glob,Grep,Bash(git status:*),Bash(git diff:*),Bash(git log:*),Bash(git add:*),Bash(git commit:*),Bash(git checkout:*),Bash(git switch:*),Bash(git branch:*),Bash(git worktree:*),Bash(git rev-parse:*),Bash(git fetch:*),Bash(bd:*),Bash(nix flake check:*),Bash(nix fmt:*),Bash(pg-connector issue show:*),Bash(pg-connector issue comment:*),Bash(pg-connector issue update:*),Bash(pg-connector issue close:*),Bash(ccpool list:*),Bash(ccpool state:*),Bash(ccpool reply:*),Bash(ccpool tail:*)"

func TestBaseAllowedTools_IsThePinnedHandlerWideSet(t *testing.T) {
	if baseAllowedTools != wantBaseAllowedTools {
		t.Fatalf("baseAllowedTools changed.\n got: %s\nwant: %s\nThe handler-wide list reaches every dontAsk pool role; widen a role or CETA instead, or update this golden together with the operator ruling that allows it", baseAllowedTools, wantBaseAllowedTools)
	}
	if Default().AllowedTools != baseAllowedTools {
		t.Errorf("Default().AllowedTools is not baseAllowedTools")
	}
}

// TestBaseAllowedTools_NeverGrantsTheStayDeniedSet pins the operator's stay-denied
// list from the pg2-r9kv7 ruling: none of these may be a handler-wide grant, in any
// prefix form. The positive control proves the splitter can see a grant at all.
func TestBaseAllowedTools_NeverGrantsTheStayDeniedSet(t *testing.T) {
	grants := SplitAllowedTools(baseAllowedTools)
	if !hasGrant(grants, "Bash(ccpool list:*)") {
		t.Fatal("positive control failed: SplitAllowedTools cannot see Bash(ccpool list:*)")
	}
	stayDenied := []string{
		"curl", "find", "sed", "awk", "xargs", "env", "bash", "sh", "zsh", "python", "python3", "perl",
		"node", "dolt", "launchctl", "tmux", "sudo", "osascript",
		"git -c", "git push", "git reset", "git clean", "git -C", "gh pr edit",
		"pg-connector issue create",
		// a whole-CLI prefix also grants the mutating verbs
		"pg-connector", "ccpool", "pg-router", "work-report", "pg-desk", "pjira",
	}
	for _, g := range grants {
		inner, ok := strings.CutPrefix(g, "Bash(")
		if !ok {
			continue
		}
		inner = strings.TrimSuffix(inner, ")")
		for _, denied := range stayDenied {
			if inner == denied+":*" || inner == denied {
				t.Errorf("handler-wide grant %q is on the stay-denied list (%q)", g, denied)
			}
		}
	}
}

// TestBaseAllowedTools_HoldsNoTriagerOnlyVerb: the W7 verb-exact reads are granted to
// the two escalation triager roles only, so none may appear handler-wide.
func TestBaseAllowedTools_HoldsNoTriagerOnlyVerb(t *testing.T) {
	grants := SplitAllowedTools(baseAllowedTools)
	for _, w7 := range []string{
		"Bash(pg-connector issue list:*)", "Bash(pg-connector issue deps:*)", "Bash(pg-connector issue comments:*)",
		"Bash(pg-connector alert list:*)", "Bash(pg-connector config validate:*)",
		"Bash(ccpool capacity:*)", "Bash(ccpool doctor:*)",
		"Bash(work-report status:*)", "Bash(work-report pull:*)",
		"Bash(pg-router gate list:*)", "Bash(pg-router config --show:*)",
	} {
		if hasGrant(grants, w7) {
			t.Errorf("handler-wide list carries the triager-only grant %q", w7)
		}
	}
}

func hasGrant(grants []string, want string) bool {
	for _, g := range grants {
		if g == want {
			return true
		}
	}
	return false
}

package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestZiprecruiterPluginCommands pins pg2-cjfpy.4 end to end: the command
// forms the ZR marketplace plugins (gh-stack, daily-focus,
// local-alert-triage, zr-refactor) instruct grade Approve under the real
// registry and policies, and the forms that need a new policy judgment or
// collide with an explicit ruling stay Abstain. The same forms are protected
// for the R4 cutover replay by internal/goldencorpus/testdata/corpus.json rows
// tagged "zr-plugin".
func TestZiprecruiterPluginCommands(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	run := func(command string, vetted ...string) evalcontract.Response {
		return Evaluate(evalcontract.Request{Command: command, CWD: root, ProjectRoot: root, VettedHosts: vetted}, reg, DefaultPolicies(), DefaultGraphPolicies())
	}

	approve := []string{
		// gh-stack
		"gh extension list | cut -f1 | grep -qxF 'gh stack'",
		"gh auth status",
		"gh stack view --json",
		"gh stack init auth api frontend",
		"gh stack init --base develop branch-a branch-b",
		"gh stack add api-routes",
		`gh stack add -Am "Add API routes" api-routes`,
		"gh stack rebase",
		"gh stack rebase --upstack",
		"gh stack rebase --continue",
		"gh stack rebase --abort --remote origin",
		"gh stack up 3",
		"gh stack down",
		"gh stack top",
		"gh stack bottom",
		"gh stack trunk",
		"gh stack checkout 42",
		"gh stack checkout https://github.com/owner/repo/pull/42",
		"gh stack checkout feature-auth",
		// daily-focus
		"df-resolve-focus 2026-10-04",
		"df-resolve-focus --any --status open",
		"df-split-blockers pg2-abc",
		"df-jira-refs pg2-abc pg2-def",
		"pg-connector pr show owner/name#12",
		"df-survey --date 2026-10-04 --cap 6 --jql 'assignee = currentUser()'",
		"df-survey --allow-existing --date 2026-10-04",
		"df-survey --deep --manifest m.json.gated.json --in-plan",
		`echo ok | df-survey --apply-gate --manifest m.json`,
		// local-alert-triage
		"lat-survey",
		"lat-survey --out lat.json",
		"df -h",
		// zr-refactor
		"rc-probe . proj --spec spec.json --app-bead pg2-abc",
		"rc-probe . proj --spec spec.json --first-only",
		"rc-sentinel write foo",
		"rc-sentinel check foo 1759600000",
		"rc-sentinel retire all 1759600000",
		"rc-preflight --list",
		"rc-preflight --verify . sess-1-rc",
		"rc-preflight --release sess-1-rc",
		"rc-preflight sess-1-rc",
		"rc-branch . feature/foo",
		"pg-desk show 123 --json",
		"pg-desk wip off 123",
		"pg-desk wip on 123",
		"pjira issue FINDEV-123",
		"pjira search --jql 'assignee = currentUser()' --limit 20",
		"pjira auth-status",
		// pg2-maars class D: existing operations only (push-lease, draft PR
		// creation, tracker-write on beads).
		"gh stack push",
		"gh stack push --remote origin",
		"gh stack submit --auto",
		"gh stack submit --auto --remote origin",
		"gh stack sync",
		"gh stack sync --prune",
		"gh stack sync --remote origin",
		"gh stack unstack --local",
		"df-close-focus pg2-abc 2026-10-04 --notes-file notes.jsonl --summary-file summary.txt --reason 'day done' --actor sess-1",
		"df-wire pg2-abc --manifest deep.json --actor sess-1",
		"df-wire pg2-abc --manifest deep.json --drop p1,j2 --merge p3=p4 --dry-run --actor sess-1",
		`echo '[]' | df-wire pg2-abc --records - --actor sess-1`,
		"df-deferred write pg2-abc --manifest items.json --actor sess-1",
		"df-deferred write pg2-abc --manifest items.json --dry-run --actor sess-1",
		"df-deferred read pg2-abc",
		"df-pull --actor sess-1",
		"df-pull 2 --actor sess-1",
		"df-pull p3 j2 --dry-run --actor sess-1",
		"lat-wire --decisions d.json --actor sess-1",
		`echo '{}' | lat-wire --decisions - --actor sess-1`,
		"rc-claim rc:foo rc-fix --actor sess-1-rc",
		"rc-claim rc:foo rc-scan --app rc-app:x --exclude-app rc-app:y --actor sess-1-rc",
		"rc-park pg2-abc 'stuck on a merge conflict' --fingerprint a.kt:x --actor sess-1-rc",
		"rc-park pg2-abc 'unpushed work' --fingerprint a.kt:x --unpushed --isolation 'wt feature/x' --actor sess-1-rc",
		"rc-fp pg2-app a.kt:x 'fixed in abc123' --actor sess-1-rc",
	}
	for _, cmd := range approve {
		t.Run("approve/"+cmd, func(t *testing.T) {
			if resp := run(cmd); resp.Decision != evalcontract.Approve {
				t.Errorf("decision = %s, want approve (reason: %s)", resp.Decision, resp.Reason)
			}
		})
	}

	// The lat-researcher curl to the local Grafana proxy is approved only
	// because the OPERATOR vetted that host (rules.json vettedHosts); without
	// the vetting it abstains -- the schema alone never vouches for a host.
	t.Run("curl/vetted-host", func(t *testing.T) {
		const cmd = "curl -s http://otel.phillipg.localhost/api/alertmanager/grafana/api/v2/alerts"
		if resp := run(cmd, "otel.phillipg.localhost"); resp.Decision != evalcontract.Approve {
			t.Errorf("vetted: decision = %s, want approve (reason: %s)", resp.Decision, resp.Reason)
		}
		if resp := run(cmd); resp.Decision != evalcontract.Abstain {
			t.Errorf("unvetted: decision = %s, want abstain", resp.Decision)
		}
	})

	abstain := []string{
		// Still need a NEW effectpolicy judgment (Jira write, PR metadata
		// write, stack-object delete), an undocumented draft default, or a
		// child-command interpreter: held for the gate-5 ruling (pg2-maars).
		"gh stack link branch-a branch-b",
		"gh stack unstack",
		"gh stack unstack 7",
		"gh stack submit",
		"gh stack push --remote https://example.com/x.git",
		"gh pr edit 12 --add-assignee @me",
		"pjira comment FINDEV-123 hi",
		"pjira create --project FINDEV",
		"pjira transition FINDEV-123 Done",
		"rc-publish . feature/foo title body.md",
		"rc-validate . proj --lock-wait 600 --run-timeout 1800 -- ./gradlew test",
		// Class D shapes the scripts do not instruct.
		"df-wire pg2-abc --force",
		"rc-claim rc:foo rc-fix --bogus --actor sess-1-rc",
		// Collide with an explicit REJECT/ABSTAIN ruling: listed, not overridden.
		"gh stack merge --yes",
		"gh stack merge 7 --yes --squash",
		"gh stack submit --auto --open",
		"gh stack link --open branch-a branch-b",
		// Shapes the plugins do not instruct.
		"gh stack modify --abort",
		"gh extension install github/gh-stack",
		"gh auth status --show-token",
		"pg-desk show 123 --refresh",
		"rc-preflight --force-release 1",
		"rc-sentinel purge foo",
	}
	for _, cmd := range abstain {
		t.Run("abstain/"+cmd, func(t *testing.T) {
			if resp := run(cmd); resp.Decision == evalcontract.Approve {
				t.Errorf("decision = approve, want not-approve (reason: %s)", resp.Reason)
			}
		})
	}
}

package engine_test

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestColimaPlutilAlwaysSafeNarrowing drives the REAL composed rule chain for the
// pg2-ul2nt5 narrowing (operator ruling on pg2-usuewr, 2026-10-28): `colima ssh --
// CMD` recurses on CMD, plutil write verbs need a writable-zone target, and
// everything else the ruling kept approved (non-exec colima verbs, plutil reads,
// nix store delete/gc/repair, launchctl, sfltool) is unchanged.
func TestColimaPlutilAlwaysSafeNarrowing(t *testing.T) {
	home := "/Users/testuser"
	t.Setenv("HOME", home)
	t.Setenv("WORKSPACE_ROOT", home+"/workspace")
	projectRoot := home + "/workspace/my-project"
	eng := buildFullEngine(projectRoot, projectRoot)
	inZone := projectRoot + "/Info.plist"

	cases := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		// colima: non-exec verbs stay approved with no inner command to judge.
		{"colima status", "colima status", hookio.Approve},
		{"colima list", "colima list", hookio.Approve},
		{"colima version", "colima version", hookio.Approve},
		{"colima start with flags", "colima start --cpu 4 --memory 8", hookio.Approve},
		{"colima stop with profile", "colima stop -p dev", hookio.Approve},
		{"colima help", "colima --help", hookio.Approve},
		// colima ssh: the inner command decides.
		{"colima ssh approved inner", "colima ssh -- ls /tmp", hookio.Approve},
		{"colima ssh profile approved inner", "colima ssh -p dev -- ls /tmp", hookio.Approve},
		{"colima ssh quoted script approved", "colima ssh -- 'ls /tmp'", hookio.Approve},
		{"colima ssh unapproved inner rm", "colima ssh -- rm -rf /", hookio.NoOpinion},
		{"colima ssh unapproved inner unknown", "colima ssh -- some-unknown-tool --go", hookio.NoOpinion},
		{"colima ssh quoted script chains rm", "colima ssh -- 'ls /tmp; rm -rf /'", hookio.NoOpinion},
		{"colima ssh multiword metachar", "colima ssh -- ls '/tmp;' rm", hookio.NoOpinion},
		{"colima ssh bare interactive", "colima ssh", hookio.NoOpinion},
		{"colima ssh no dashdash", "colima ssh ls", hookio.NoOpinion},
		{"colima ssh empty command", "colima ssh --", hookio.NoOpinion},
		{"colima ssh unknown flag", "colima ssh --bogus -- ls /tmp", hookio.NoOpinion},
		{"colima unknown flag hides verb", "colima --bogus ssh -- ls /tmp", hookio.NoOpinion},

		// plutil: reads are unconditional.
		{"plutil -p", "plutil -p " + inZone, hookio.Approve},
		{"plutil -lint", "plutil -lint " + inZone, hookio.Approve},
		{"plutil -type", "plutil -type K " + inZone, hookio.Approve},
		{"plutil -extract stdout", "plutil -extract K raw " + inZone, hookio.Approve},
		{"plutil -p outside zone", "plutil -p /etc/hosts", hookio.Approve},
		{"plutil -extract to stdout", "plutil -extract K raw -o - " + inZone, hookio.Approve},
		// plutil writes: writable zone approves, otherwise abstain.
		{"plutil -replace in zone", "plutil -replace K -string v " + inZone, hookio.Approve},
		{"plutil -replace path value in zone", "plutil -replace K -string /etc/foo " + inZone, hookio.Approve},
		{"plutil -insert in zone", "plutil -insert K -bool true " + inZone, hookio.Approve},
		{"plutil -insert dictionary in zone", "plutil -insert K -dictionary " + inZone, hookio.Approve},
		{"plutil -remove in zone", "plutil -remove K " + inZone, hookio.Approve},
		{"plutil -convert in zone", "plutil -convert xml1 " + inZone, hookio.Approve},
		{"plutil -convert -o in zone", "plutil -convert xml1 -o " + projectRoot + "/out.plist " + inZone, hookio.Approve},
		{"plutil -replace outside zone", "plutil -replace K -string v /etc/foo.plist", hookio.NoOpinion},
		{"plutil -remove outside zone", "plutil -remove K /etc/foo.plist", hookio.NoOpinion},
		{"plutil -convert -o outside zone", "plutil -convert xml1 -o /etc/out.plist " + inZone, hookio.NoOpinion},
		{"plutil -extract -o outside zone", "plutil -extract K xml1 -o /etc/out.plist " + inZone, hookio.NoOpinion},
		{"plutil -create outside zone", "plutil -create xml1 /etc/new.plist", hookio.NoOpinion},
		{"plutil write dynamic target", "plutil -replace K -string v $TARGET", hookio.NoOpinion},
		{"plutil -replace missing value", "plutil -replace K -string", hookio.NoOpinion},

		// nix store verbs: ruling keeps them approved; this bead does not touch the nix rule.
		{"nix store gc", "nix store gc", hookio.Approve},
		{"nix store delete", "nix store delete /nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-x", hookio.Approve},
		{"nix store repair", "nix store repair /nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-x", hookio.Approve},

		// launchctl and sfltool: deliberately unchanged (open operator question).
		{"launchctl list", "launchctl list", hookio.Approve},
		{"launchctl bootstrap", "launchctl bootstrap gui/501 /Library/LaunchAgents/x.plist", hookio.Approve},
		{"sfltool dumpbtm", "sfltool dumpbtm", hookio.Approve},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &hookio.HookInput{ToolName: "Bash", CWD: projectRoot, ToolInput: makeBashJSON(tc.command)}
			got := eng.EvaluateHook(in)
			if got.Decision != tc.want {
				t.Errorf("%q got %s (%s: %s) want %s", tc.command, got.Decision, got.Module, got.Reason, tc.want)
			}
		})
	}
}

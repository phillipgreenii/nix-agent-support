package git

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// THE ALTERNATE-TRANSPORT FAMILY, SETTLED UNDER ONE RULING (pg2-qi1jo).
//
// pg2-szadj left five keys approved as "genuine sinks and loosenings, but on a server-side
// or alternate-transport path this workflow does not use", and asked a later reader to add
// them UNDER ONE RULING rather than piecemeal. pg2-6c85x then MEASURED `GIT_PROXY_COMMAND`
// reaching an exec sink and DECLINED to screen it, precisely so as not to split that
// family across two routes. pg2-qi1jo is the ruling: all five keys are screened, and the
// env half moves in the same change.
//
// EVERY KEY IS MEASURED, git 2.54.0, 2026-08-14 (scripts/probe-pg2-qi1jo.sh):
//
//	core.gitProxy               `ls-remote git://invalid.invalid/x.git` ran the marker as `invalid.invalid 9418`
//	remote.<n>.uploadpack       `ls-remote <remote>` and `fetch <remote>` ran it — the FETCHING side, i.e. this machine
//	remote.<n>.receivepack      `push <remote> HEAD:refs/heads/x` ran it — the PUSHING side
//	uploadpack.packObjectsHook  ran from GLOBAL config (ignored at repository level, by git's own documented safety measure)
//	protocol.<n>.allow          `ls-remote 'ext::<marker> %S'` ran it WITH `protocol.ext.allow=always` and NOT without
//
// TWO OF THOSE REFUTE THE SURVEY'S "SERVER-SIDE" READING, which is why this ruling screens
// instead of confirming the approval: `remote.<n>.uploadpack` and `remote.<n>.receivepack`
// run LOCALLY whenever the remote is a local path, which is the everyday shape in a `pn`
// workforest.
//
// THE TESTS ARE RELATIONS, not literals, for the reason configenv_test.go states — and here
// there is a second reason: the family spans TWO classes, so its members do NOT share one
// verdict. `protocol.<n>.allow` is an interlock and the other four are sinks, and the claim
// is that each is answered like ITS OWN CLASS, which is what makes "one ruling" checkable
// rather than a slogan.

// transportSinkKeys are the family's configSink members, spelled as a real invocation would
// spell them (with a subsection where the key takes one).
var transportSinkKeys = []string{
	"core.gitProxy",
	"remote.origin.uploadpack",
	"remote.origin.receivepack",
	"uploadpack.packObjectsHook",
}

// TestGit_TransportFamily_EveryMemberIsGatedInItsOwnClass is the "one ruling" assertion:
// each member resolves in the REAL gatedConfigKeys table, in the class its measured
// mechanism warrants. It reads the table rather than the verdicts, so it catches a member
// being dropped even if some other key happens to keep the behavioural rows green.
func TestGit_TransportFamily_EveryMemberIsGatedInItsOwnClass(t *testing.T) {
	want := map[string]configGateClass{
		// Sinks: git EXECUTES the value.
		"core.gitProxy":              configSink,
		"remote.origin.uploadpack":   configSink,
		"remote.origin.receivepack":  configSink,
		"uploadpack.packObjectsHook": configSink,
		// Loosening: git REFUSES the `ext::` transport by default and this removes the
		// refusal. It is NOT a sink — it names no program — and classing it as one would
		// misdescribe the mechanism even though both classes happen to answer Ask today.
		"protocol.ext.allow": configInterlock,
	}
	for key, wantClass := range want {
		_, id, ok := configKeyID(key)
		if !ok {
			t.Errorf("%s: configKeyID cannot resolve it — the key is not key-shaped", key)
			continue
		}
		got, gated := gatedConfigKeys[id]
		if !gated {
			t.Errorf("%s (id %q) is NOT in gatedConfigKeys — pg2-qi1jo settled the whole alternate-transport family under ONE ruling, and dropping a member re-creates the piecemeal state pg2-szadj's instruction existed to prevent", key, id)
			continue
		}
		if got != wantClass {
			t.Errorf("%s (id %q) is class %d, want %d — the class IS the mechanism claim: the four sinks are values git executes, while protocol.<n>.allow removes a refusal", key, id, got, wantClass)
		}
	}
}

// TestGit_TransportFamily_WritesAreGatedInEverySpelling is the behavioural half. Each row is
// a spelling that shifts the key out of first operand position, which is the pg2-szadj
// defect class this table's operand scan exists to close.
func TestGit_TransportFamily_WritesAreGatedInEverySpelling(t *testing.T) {
	for _, key := range append(append([]string{}, transportSinkKeys...), "protocol.ext.allow", "protocol.allow") {
		value := "/tmp/evil"
		if key == "protocol.ext.allow" || key == "protocol.allow" {
			value = "always"
		}
		for _, cmd := range []string{
			"git config " + key + " " + value,
			"git config --global " + key + " " + value,
			"git config --local " + key + " " + value,
			"git config --add " + key + " " + value,
			"git config --unset " + key,
			"git config set " + key + " " + value,
			"git config -f .git/config " + key + " " + value,
			"git -C /tmp/repo config " + key + " " + value,
		} {
			if got := evalCmd(t, cmd); got.Decision == hookio.Approve {
				t.Errorf("cmd %q: got APPROVE (%s) — every member of the alternate-transport family is gated since pg2-qi1jo, and each was MEASURED running a marker on git 2.54.0", cmd, got.Reason)
			}
		}
	}
}

// TestGit_TransportFamily_ConfigKeyAndEnvSpellingAgree is the acceptance criterion the bead
// names literally: the config-key spelling and the env spelling reach the same verdict,
// asserted as a relation.
//
// It is the same relation programenv_test.go asserts for the whole table, restated here for
// the ONE variable this bead moved — because that table's tests iterate it, so a
// GIT_PROXY_COMMAND that were silently dropped again would take its own coverage with it.
func TestGit_TransportFamily_ConfigKeyAndEnvSpellingAgree(t *testing.T) {
	if twin, screened := gitProgramEnvVars["GIT_PROXY_COMMAND"]; !screened {
		t.Fatal("GIT_PROXY_COMMAND is not in gitProgramEnvVars — pg2-qi1jo screened it as the env half of the family ruling; declining it again leaves a MEASURED exec sink open on the env route while the config route is gated, which is the split the ruling closed")
	} else if twin != "core.gitProxy" {
		t.Errorf("GIT_PROXY_COMMAND's recorded twin is %q, want core.gitProxy — the twin is what makes the screen's justification checkable against gatedConfigKeys", twin)
	}
	for _, sub := range approveClassSubcommands {
		env := evalCmd(t, envProg("GIT_PROXY_COMMAND", "/tmp/evil", sub))
		argv := evalCmd(t, dashC("core.gitProxy", "/tmp/evil", sub))
		if env.Decision != argv.Decision {
			t.Errorf("`git %s`: the GIT_PROXY_COMMAND spelling got %s (%s) but `-c core.gitProxy` got %s (%s) — one exec sink, two spellings, and settling the family means they MUST agree (pg2-qi1jo)",
				sub, env.Decision, env.Reason, argv.Decision, argv.Reason)
		}
		if env.Decision == hookio.Approve {
			t.Errorf("`git %s`: the GIT_PROXY_COMMAND spelling got APPROVE — git runs this value as the transport proxy; measured `invalid.invalid 9418` on git 2.54.0", sub)
		}
	}
}

// TestGit_TransportFamily_DoesNotOverreach pins the boundary. Every row is a spelling git
// itself treats as ordinary, so gating it would be a false prompt on real traffic — and
// without these rows the ruling could have been a blanket gate on the whole `remote.`,
// `uploadpack.` and `protocol.` sections with every test above still green.
func TestGit_TransportFamily_DoesNotOverreach(t *testing.T) {
	rows := []struct {
		cmd  string
		want hookio.Decision
		why  string
	}{
		// READS are not gated — the whole table gates writes only.
		{"git config --get core.gitProxy", hookio.Approve, "a read is not a write"},
		{"git config --get remote.origin.uploadpack", hookio.Approve, "same"},
		{"git config --get-regexp '^protocol\\.'", hookio.Approve, "same"},
		// Neighbouring keys in the same sections that this ruling does NOT cover.
		{"git config remote.origin.fetch +refs/heads/*:refs/remotes/origin/*", hookio.Approve, "a refspec names no program"},
		{"git config remote.origin.prune true", hookio.Approve, "an ordinary remote setting"},
		{"git config remote.origin.tagOpt --no-tags", hookio.Approve, "same"},
		{"git config uploadpack.allowFilter true", hookio.Approve, "a capability toggle, not the pack-objects hook"},
		{"git config protocol.version 2", hookio.Approve, "the wire version is not the allow-list"},
		{"git config core.gitProxyish /tmp/evil", hookio.Approve, "a longer variable name is a different key"},
		// The transport-adjacent ORDINARY traffic this rule exists to keep flowing.
		{"git fetch origin", hookio.Approve, "an ordinary fetch"},
		{"git ls-remote origin", hookio.Approve, "an ordinary read"},
		// TEXT IS NOT AN OPERATION — the pg2-5b901 class.
		{`git commit -m "settle core.gitProxy + GIT_PROXY_COMMAND under one ruling (pg2-qi1jo)"`, hookio.Approve, "a mention in a commit message is text"},
		{`git commit -m "git config core.gitProxy /tmp/evil measured allow before the fix"`, hookio.Approve, "same, with an = -free key/value in the text"},
	}
	for _, row := range rows {
		if got := evalCmd(t, row.cmd); got.Decision != row.want {
			t.Errorf("cmd %q: got %s (%s), want %s — %s", row.cmd, got.Decision, got.Reason, row.want, row.why)
		}
	}
}

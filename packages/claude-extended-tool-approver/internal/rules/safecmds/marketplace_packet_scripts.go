package safecmds

import (
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// plan-decompose's packet scripts, invoked by ABSOLUTE marketplace path
// (pg2-zp3fl; docs/adr/0099-ceta-marketplace-plugin-scripts-are-a-trusted-executable-identity.md).
//
// WHY THIS LIVES IN THE LEGACY RULE CHAIN. ADR 0099 taught only the effect
// engine (patheval.ResolveTrustedExecutable) to trust a marketplace plugin
// script. The effect engine decides `evaluate --corpus` replays, NOT the live
// PreToolUse hook, which still runs setup.RuleChain until production cutover
// (see the cmddesc registry commits "the live hook still runs the legacy
// internal/rules chain until production cutover"). The corpus rows therefore
// replayed as approve while the applied hook abstained on the identical
// command. This branch is the live-hook half of ADR 0099, in the same
// argument-aware style as handoff-create (pg2-8ksxn).
//
// WHAT IS APPROVED. Only the three scripts the decomposers are instructed to
// call, only when the argv0 is a verified marketplace plugin script
// (patheval.IsMarketplacePluginScript: canonical HOME-spelled path, marketplace
// entry resolving to a direct /nix/store child named "-claude-marketplace-",
// realpath staying inside that derivation), and only with the script's exact
// flag surface (mirrors the cmddesc schemas in registry_plugin_tools.go):
//
//   - a flag outside the surface, a glued --flag=value, a positional, or a
//     missing value is refused (the script would only reject it anyway);
//   - a value carrying a LIVE shell expansion is refused (a quoted-literal `$`
//     is fine; ArgLiveExpansion is the provenance), so a variable cannot smuggle
//     in a path or extra words;
//   - path-valued flags are held to the zone checks a plain read/write gets:
//     --body-file/--acceptance-file/--design/--in through readPathIssue (the
//     scripts copy their content into a bead or an output file, so they are
//     read primitives), --out through the writable-zone check;
//   - --metadata beginning with "@" names a file to read, so it is refused.
//
// A variable argv0 (`S=<dir>; $S/create-packet.sh`) never reaches this branch
// as a trusted identity: its Executable starts with "$", so
// IsMarketplacePluginScript is false (ADR 0099, item 3). Its basename still
// matches, which is why the identity check, not the basename, gates the branch.
type packetScriptSpec struct {
	bools   map[string]bool
	literal map[string]bool // free-text values, no path semantics
	read    map[string]bool // value is a file the script reads
	write   map[string]bool // value is a file the script creates
	// metadata marks the one flag whose value is JSON or "@file".
	metadata map[string]bool
}

var packetScripts = map[string]packetScriptSpec{
	"create-packet.sh": {
		bools:    map[string]bool{"--allow-inherit-labels": true, "-h": true, "--help": true},
		literal:  map[string]bool{"--parent": true, "--title": true, "--acceptance": true, "--label": true, "--actor": true},
		read:     map[string]bool{"--body-file": true, "--acceptance-file": true},
		metadata: map[string]bool{"--metadata": true},
	},
	"assemble-packet.sh": {
		bools: map[string]bool{"-h": true, "--help": true},
		read:  map[string]bool{"--design": true, "--in": true},
		write: map[string]bool{"--out": true},
	},
	"update-packet.sh": {
		bools:   map[string]bool{"-h": true, "--help": true},
		literal: map[string]bool{"--id": true, "--actor": true},
		read:    map[string]bool{"--body-file": true, "--acceptance-file": true},
	},
}

// marketplacePacketScript returns the spec for pc's executable when it is one of
// the packet scripts AND a verified marketplace plugin script.
func marketplacePacketScript(executable string) (packetScriptSpec, bool) {
	if !strings.HasPrefix(executable, "/") {
		return packetScriptSpec{}, false
	}
	spec, ok := packetScripts[executable[strings.LastIndex(executable, "/")+1:]]
	if !ok || !patheval.IsMarketplacePluginScript(executable) {
		return packetScriptSpec{}, false
	}
	return spec, true
}

// marketplacePacketScriptIssue judges one packet-script invocation; it returns
// ("", _) when it may be approved, else a reason fragment plus its category.
func marketplacePacketScriptIssue(spec packetScriptSpec, args []string, live []bool, pe *patheval.PathEvaluator, vars map[string][]string) (string, hookio.RefusalCategory) {
	isLive := func(i int) bool { return i >= len(live) || live[i] }
	for i := 0; i < len(args); i++ {
		a := args[i]
		if spec.bools[a] {
			continue
		}
		isValueFlag := spec.literal[a] || spec.read[a] || spec.write[a] || spec.metadata[a]
		if !isValueFlag {
			return "has an argument outside its closed flag surface: " + a, hookio.RefusalCategoryUnspecified
		}
		if i+1 >= len(args) {
			return "flag " + a + " is missing its value", hookio.RefusalCategoryUnspecified
		}
		i++
		v := args[i]
		switch {
		case spec.read[a]:
			if issue, cat := readPathIssue([]string{v}, pe, "", true, vars); issue != "" {
				return a + " " + issue, cat
			}
		case spec.write[a]:
			if isLive(i) || argHasDynamicExpansion(v) {
				return a + " has a dynamically-expanded path " + v, hookio.RefusalCategoryUnspecified
			}
			if !pe.Evaluate(v).CanWrite() {
				return a + " references non-writable path " + v, hookio.RefusalCategoryUnspecified
			}
		case spec.metadata[a]:
			if isLive(i) {
				return a + " has a dynamically-expanded value", hookio.RefusalCategoryUnspecified
			}
			if strings.HasPrefix(v, "@") {
				return a + " " + v + " names a file to read", hookio.RefusalCategoryUnspecified
			}
		default: // literal
			if isLive(i) {
				return a + " has a dynamically-expanded value", hookio.RefusalCategoryUnspecified
			}
		}
	}
	return "", hookio.RefusalCategoryUnspecified
}

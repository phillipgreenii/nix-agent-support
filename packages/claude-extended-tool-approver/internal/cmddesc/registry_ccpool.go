package cmddesc

// ccpool (packages/ccpool): the Claude Code session pool the pg-router workers
// and the operator's pool-triage skills drive. `ccpool [--pool <dir>] <command>`.
//
// # What approves
//
// The read-only listing and status verbs print the pool's store and live state
// and change nothing: list, state, tail, result, consumption, capacity,
// doctor, version, and `meta get` / `meta list`. Each approves with its own
// flags only; --pool names the pool directory the command reads, so it is a
// PathRead judged by the path policy (a pool under ~/.local/state is a
// read-only root, anything outside the zones abstains).
//
// # What does NOT approve
//
//   - trust pre-trusts a folder: it writes the folder's trust entry into
//     ~/.claude.json (cmd/ccpool/trust.go: trust.EnsureTrusted on
//     $HOME/.claude.json), the agent's own settings file. That is a PathModify of
//     the home directory's ~/.claude.json, which the path policy does not approve
//     (the R7/P5 rule: an agent never rewrites the agent's own configuration).
//     The directory operand is a literal; the write is stated as an implicit
//     effect so it cannot be missed.
//   - new, reply, attach and attend drive ANOTHER agent session (they launch
//     one, send it a prompt, or attach the operator's terminal to its tmux
//     pane). No effect kind models launching or steering a different agent's
//     turn, so they are deliberately ABSENT and abstain, --help included.
//   - cancel, close, reap, reap-all, emit-gauge, hook and `meta set` / `meta rm`
//     change pool state (interrupt a turn, stop a session, delete store rows,
//     emit metrics, record a turn event, edit tags) and no instructed skill
//     needs them from an unattended worker: absent, so they abstain.
//   - capacity --emit-metrics and --pool-dir (an OTLP gauge emission to the
//     recorder) stay unmodeled, so a call carrying them abstains.
//
// Only the double-dash spelling is modeled (ccpool uses Go's flag package, which
// also accepts a single dash); the single-dash form abstains.
func ccpoolReadVerb(name, desc string, flags map[string]FlagSpec, pos PositionalSpec) CommandSchema {
	return CommandSchema{
		Name:         name,
		Provenance:   "ccpool " + name + " --help and ccpool --help (" + desc + "), ccpool 0.0.0-48e55a29, this host 2026-11-17",
		Flags:        mergeFlags(map[string]FlagSpec{"-h": inert, "--help": inert}, flags),
		Positionals:  pos,
		Stdin:        StdinNever,
		Stdout:       StdoutContent,
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
	}
}

var ccpoolSchema = func() CommandSchema {
	id := PositionalSpec{Leading: []OperandRole{Literal}, Rest: Unmodeled}
	none := PositionalSpec{Rest: Unmodeled}
	return CommandSchema{
		Name:       "ccpool",
		Provenance: "ccpool --help (usage: ccpool [--pool <dir>] <command> [<args>]), ccpool 0.0.0-48e55a29, this host 2026-11-17",
		Flags: map[string]FlagSpec{
			"-h": inert, "--help": inert,
			"--pool": {Arity: ArityOne, Operand: PathRead},
		},
		UnknownFlag:  UnknownFlagInsufficient,
		EndOfOptions: true,
		Subcommands: map[string]CommandSchema{
			"list": ccpoolReadVerb("list", "list sessions in the pool", map[string]FlagSpec{
				"--all": inert, "--json": inert, "--no-facets": inert, "--paths": inert,
				"--state": literal1, "--filter": literal1,
			}, none),
			"state":       ccpoolReadVerb("state", "print a session's live, reconciled state", map[string]FlagSpec{"--json": inert}, id),
			"tail":        ccpoolReadVerb("tail", "print a session's recent transcript output", nil, id),
			"result":      ccpoolReadVerb("result", "resolve a previously dispatched turn's reply from its transcript anchor", nil, id),
			"consumption": ccpoolReadVerb("consumption", "print a session's token consumption and estimated cost", map[string]FlagSpec{"--json": inert}, id),
			"capacity":    ccpoolReadVerb("capacity", "report pool occupancy and free slots", map[string]FlagSpec{"--json": inert}, none),
			"doctor":      ccpoolReadVerb("doctor", "print each session's pool context and cached store state", nil, none),
			"version":     ccpoolReadVerb("version", "print the ccpool version", nil, none),
			"meta": {
				Name:         "meta",
				Provenance:   "ccpool meta usage (ccpool meta <set|get|list|rm> <external_id> [key] [value]), ccpool 0.0.0-48e55a29, this host 2026-11-17; only get and list are read-only",
				Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
				UnknownFlag:  UnknownFlagInsufficient,
				EndOfOptions: true,
				Subcommands: map[string]CommandSchema{
					"get": ccpoolReadVerb("get", "get a session's metadata key", nil,
						PositionalSpec{Leading: []OperandRole{Literal, Literal}, Rest: Unmodeled}),
					"list": ccpoolReadVerb("list", "list a session's metadata", map[string]FlagSpec{"--json": inert}, id),
				},
			},
			"trust": {
				Name:        "trust",
				Provenance:  "ccpool --help (trust: pre-trust a cwd so an automated claude launch there doesn't stall on the folder-trust prompt); cmd/ccpool/trust.go writes $HOME/.claude.json, ccpool 0.0.0-48e55a29, this host 2026-11-17",
				Flags:       map[string]FlagSpec{"-h": inert, "--help": inert},
				Positionals: PositionalSpec{Rest: Literal},
				ImplicitEffects: []ImplicitEffect{
					{Role: PathModify, Target: "~/.claude.json"},
				},
				Stdin:        StdinNever,
				Stdout:       StdoutMetadata,
				UnknownFlag:  UnknownFlagInsufficient,
				EndOfOptions: true,
			},
		},
	}
}()

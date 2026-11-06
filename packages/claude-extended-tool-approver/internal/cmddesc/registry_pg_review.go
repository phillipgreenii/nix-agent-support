package cmddesc

import "strings"

// pg-review (packages/pg-review): the self-review tool whose unattended analyst
// session runs `pg-review agent q <provider> <query-name> [--arg value ...]` and
// nothing else (design: the analyst's allowlist is the reading tools plus that
// one command; the ccpool analyst never calls `agent file`, the runner reads its
// final message and files it).
//
// Decision per subcommand:
//
//	agent q     APPROVE. The query broker accepts only read-only query names the
//	            provider lists for the run's lens and refuses --scope, --from and
//	            --to itself, so the arguments are inert data to it. The one
//	            guarantee the hook adds is the shape of those arguments: every
//	            operand after `q` must be a plain token (allowedLiteralSets
//	            "pg-review-agent-arg"), so a shell metacharacter inside a quoted
//	            argument is refused, as is a runtime expansion. The provider is a
//	            [a-z0-9-]+ name ("pg-review-provider").
//	agent file  SPECCED, NOT APPROVED. It files findings into the review ledger,
//	            which an unattended analyst must not do; the positional (the
//	            findings path, or - for stdin) is modeled Unmodeled, so any use
//	            abstains and the operator keeps the decision.
//	every other subcommand (agent context, human *, job *): absent, so abstain.
//
// Every flag and fact is cited from the tool's own --help in
// internal/embeddedspecs/data/pg-review.json.

var pgReviewHelpFlags = map[string]FlagSpec{"-h": inert, "--help": inert}

func pgReviewFlags(extra map[string]FlagSpec) map[string]FlagSpec {
	m := map[string]FlagSpec{"--output": literal1}
	for k, v := range pgReviewHelpFlags {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

var pgReviewSchema = CommandSchema{
	Name:       "pg-review",
	Provenance: "pg-review --help ('Self-review: find, rule on and track improvements to how agents work'), packages/pg-review built from this repo 2026-11-06",
	Flags: map[string]FlagSpec{
		"-h": inert, "--help": inert, "-v": inert, "--version": inert,
		"--output": literal1,
	},
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
	Subcommands: map[string]CommandSchema{
		"agent": {
			Name:         "agent",
			Provenance:   "pg-review agent --help: 'Commands for the analyst agent: query evidence, file findings'",
			Flags:        pgReviewFlags(nil),
			UnknownFlag:  UnknownFlagInsufficient,
			EndOfOptions: true,
			Subcommands: map[string]CommandSchema{
				"q": {
					Name:       "q",
					Provenance: "pg-review agent q --help: 'Run a read-only evidence query through the broker'",
					Flags:      pgReviewFlags(map[string]FlagSpec{"--run": literal1}),
					// The first positional (the provider) ends option parsing: every
					// later `--arg` belongs to the provider, and the command parses
					// them by hand (DisableFlagParsing in its source).
					PositionalsEndOptions: true,
					Positionals: PositionalSpec{
						Leading: []OperandRole{AllowedLiteral("pg-review-provider")},
						Rest:    AllowedLiteral("pg-review-agent-arg"),
						MinRest: 1,
					},
					Stdin:        StdinNever,
					Stdout:       StdoutContent,
					UnknownFlag:  UnknownFlagInsufficient,
					EndOfOptions: true,
				},
				"file": {
					Name:       "file",
					Provenance: "pg-review agent file --help: 'Validate and file a findings document (- reads stdin)'",
					Flags: pgReviewFlags(map[string]FlagSpec{
						"--dry-run": inert, "--run": literal1,
					}),
					Positionals:  PositionalSpec{Rest: Unmodeled},
					Stdin:        StdinNever,
					Stdout:       StdoutContent,
					UnknownFlag:  UnknownFlagInsufficient,
					EndOfOptions: true,
				},
			},
		},
	},
}

// pgReviewToken reports whether tok is a plain argument for `pg-review agent q`:
// non-empty, and free of every shell metacharacter and control character. It
// refuses ; & | < > ( ) $ backtick backslash, both quote characters, `{ }`,
// glob and history characters, newline and every other control character.
// Spaces are allowed (an argument value may be a phrase); a leading `-` is
// allowed because provider flags (`--tool Bash`) arrive as ordinary arguments.
func pgReviewToken(tok string) bool {
	if tok == "" {
		return false
	}
	if strings.ContainsAny(tok, ";&|<>()$`\\\"'{}*?[]!~#\x7f") {
		return false
	}
	for _, r := range tok {
		if r < 0x20 {
			return false
		}
	}
	return true
}

// pgReviewProvider reports whether tok is a provider name: [a-z0-9-]+.
func pgReviewProvider(tok string) bool {
	if tok == "" {
		return false
	}
	for _, r := range tok {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

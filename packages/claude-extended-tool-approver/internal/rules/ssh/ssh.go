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
//     PasswordFlagPatterns substring -> Reject.
//   - user allowlist: an explicit user (-l, -o User=, or user@host) not in
//     AllowedUsers, or conflicting users -> Reject.
//   - read-only classification: ssh with no remote command -> Ask (interactive);
//     a remote command whose every segment's executable is in ReadonlyCommands
//     (honoring ReadonlySubcommands), with no file-writing redirect (see
//     hasWriteRedirection: `2>&1` and `2>/dev/null` are fine, `> f` is not), no
//     tee and no secret path ->
//     Approve; otherwise Ask. A segment whose executable is in ReadonlyCommands
//     but which carries a configured DangerousInlineFlags flag (e.g.
//     `journalctl --vacuum-size=1G`, `sed -i`) is demoted back to Ask.
//   - scp: download from a non-secret remote path -> Approve; upload, mixed
//     local/remote, or a secret remote path -> Ask.
package ssh

import (
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

func (r *Rule) abstain() hookio.RuleResult {
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return r.abstain()
	}
	// WS2 safe default: with no injected policy data, defer entirely. WS3 wires
	// the rules.json config that flips `configured` on.
	if !r.configured {
		return r.abstain()
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return r.abstain()
	}
	for _, pc := range cmdparse.Parse(cmdStr) {
		base := filepath.Base(pc.Executable)
		switch base {
		case "sshpass":
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "sshpass wrapper is forbidden — use key-based auth",
				Module:   r.Name(),
			}
		case "ssh", "scp":
			return r.evaluateSSHScp(base, pc.Args)
		}
	}
	return r.abstain()
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

func (r *Rule) checkPasswordAuth(opts map[string]string) (hookio.RuleResult, bool) {
	for k, v := range opts {
		joined := k + "=" + v
		for _, pat := range r.passwordFlagPatterns {
			if strings.Contains(joined, pat) {
				return hookio.RuleResult{
					Decision: hookio.Reject,
					Reason:   "password-based ssh auth is forbidden: -o " + joined,
					Module:   r.Name(),
				}, true
			}
		}
	}
	return hookio.RuleResult{}, false
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
	for _, seg := range splitSegments(remoteCmd) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if !r.segmentIsReadonly(seg) {
			return hookio.RuleResult{Decision: hookio.Ask, Reason: "remote command is not a recognized read-only command: " + seg, Module: r.Name()}
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

// splitSegments splits a remote command on unquoted-agnostic top-level shell
// operators (|| && ; |). This is deliberately simpler than the full cmdparse
// splitter — the remote command is opaque text and this classification only
// needs a coarse per-segment read-only check.
func splitSegments(cmd string) []string {
	replacer := strings.NewReplacer("||", "\n", "&&", "\n", ";", "\n", "|", "\n")
	return strings.Split(replacer.Replace(cmd), "\n")
}

// segmentIsReadonly reports whether a single remote-command segment is a
// recognized read-only invocation: no file-writing redirect (hasWriteRedirection
// draws that line — stderr redirection such as `2>&1` is NOT a write), no tee, executable in
// ReadonlyCommands, and (if the command has a configured subcommand allowlist)
// its first subcommand token is allowed.
func (r *Rule) segmentIsReadonly(seg string) bool {
	if hasWriteRedirection(seg) {
		return false
	}
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return false
	}
	base := fields[0]
	if base == "tee" || !r.readonlyCommands[base] {
		return false
	}
	if allowed, ok := r.readonlySubcommands[base]; ok {
		sub := firstSubToken(fields[1:])
		if sub == "" || !allowed[sub] {
			return false
		}
	}
	// A configured dangerous inline flag demotes an otherwise read-only command
	// back to Ask: a token equal to the flag or beginning with "<flag>=" (e.g.
	// `journalctl --vacuum-size=1G`, `sed -i`) is destructive despite the command
	// being in ReadonlyCommands.
	for _, bad := range r.dangerousInlineFlags[base] {
		for _, tok := range fields[1:] {
			if tok == bad || strings.HasPrefix(tok, bad+"=") {
				return false
			}
		}
	}
	return true
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
// Scope note: `|& tee f` needs no special case here — splitSegments already
// splits on `|`, leaving a segment whose executable is `&`, which is not in
// ReadonlyCommands.
func hasWriteRedirection(seg string) bool {
	rs := []rune(seg)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '<':
			// `<>` opens the target for reading AND writing; every other input
			// form (`<`, `<<`, `<<<`, `<&N`) only reads.
			if i+1 < len(rs) && rs[i+1] == '>' {
				return true
			}
		case '>':
			// Consume the operator's remaining punctuation: `>>` (append) and
			// `>|` (clobber) still take a FILE target, so they change nothing
			// about the classification below.
			j := i + 1
			for j < len(rs) && (rs[j] == '>' || rs[j] == '|') {
				j++
			}
			if j < len(rs) && rs[j] == '&' {
				// `N>&WORD`: a bare fd number or `-` duplicates/closes a
				// descriptor and touches no file. Anything else is bash's
				// both-streams-to-file form and IS a write.
				j++
				word, next := readWord(rs, j)
				if word != "-" && !isAllDigits(word) {
					return true
				}
				i = next - 1
				continue
			}
			// Ordinary file target, spaced (`> f`) or glued (`>f`).
			for j < len(rs) && isShellSpace(rs[j]) {
				j++
			}
			word, next := readWord(rs, j)
			if strings.Trim(word, `"'`) != "/dev/null" {
				return true
			}
			i = next - 1
		}
	}
	return false
}

// readWord returns the whitespace-delimited word starting at index i and the
// index just past it. An empty word means the operator had no target.
func readWord(rs []rune, i int) (string, int) {
	j := i
	for j < len(rs) && !isShellSpace(rs[j]) {
		j++
	}
	return string(rs[i:j]), j
}

func isShellSpace(c rune) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

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

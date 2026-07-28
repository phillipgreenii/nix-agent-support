// Package ssh is a config-driven MECHANISM for classifying ssh/scp commands (a
// hook-support parity capability; SshCommandEvaluator). It follows the
// kubectl/buildtools template: the evaluation logic lives here in ceta-core,
// and all policy DATA (allowed users, read-only command allowlist, secret-path
// patterns, password-auth flag patterns) arrives via an injected Config.
//
// SAFE DEFAULT (WS2/WS3 seam): an empty Config makes the rule Abstain on every
// command. Until WS3 wires the rules.json-loaded data in (see the `// WS3:`
// marker in internal/setup/factory.go), the rule therefore never auto-approves
// or blocks — it defers. Only once a consumer supplies data does the mechanism
// classify.
//
// Mechanism (when configured):
//   - password-auth block: sshpass wrapper, or an -o option matching a
//     PasswordFlagPatterns substring -> Reject.
//   - user allowlist: an explicit user (-l, -o User=, or user@host) not in
//     AllowedUsers, or conflicting users -> Reject.
//   - read-only classification: ssh with no remote command -> Ask (interactive);
//     a remote command whose every segment's executable is in ReadonlyCommands
//     (honoring ReadonlySubcommands), with no redirect/tee and no secret path ->
//     Approve; otherwise Ask.
//   - scp: download from a non-secret remote path -> Approve; upload, mixed
//     local/remote, or a secret remote path -> Ask.
package ssh

import (
	"path/filepath"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// Config carries the consumer-specific ssh/scp policy DATA. Every field is
// data-only; the MECHANISM lives in this package. A zero Config yields
// Abstain-on-everything (the safe WS2 default).
type Config struct {
	// AllowedUsers are the ssh/scp users that may be targeted (e.g. "tcadmin").
	// An explicit user outside this set is Rejected.
	AllowedUsers []string
	// ReadonlyCommands are remote executable basenames considered read-only.
	ReadonlyCommands []string
	// ReadonlySubcommands restricts a read-only command to specific first
	// subcommands (e.g. "systemctl" -> {"status","is-active"}).
	ReadonlySubcommands map[string][]string
	// SecretPathPatterns are substrings that mark a remote path as secret.
	SecretPathPatterns []string
	// PasswordFlagPatterns are lowercased `key=value` substrings that mark an
	// -o option as enabling password auth (e.g. "passwordauthentication=yes").
	PasswordFlagPatterns []string
}

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
	secretPathPatterns   []string
	passwordFlagPatterns []string
}

// New constructs the ssh rule from cfg. A zero cfg makes the rule Abstain on
// every command (safe WS2 default).
func New(cfg Config) *Rule {
	r := &Rule{
		allowedUsers:         toSet(cfg.AllowedUsers),
		readonlyCommands:     toSet(cfg.ReadonlyCommands),
		readonlySubcommands:  map[string]map[string]bool{},
		secretPathPatterns:   cfg.SecretPathPatterns,
		passwordFlagPatterns: lowerAll(cfg.PasswordFlagPatterns),
	}
	for cmd, subs := range cfg.ReadonlySubcommands {
		r.readonlySubcommands[cmd] = toSet(subs)
	}
	r.configured = len(cfg.AllowedUsers) > 0 || len(cfg.ReadonlyCommands) > 0 ||
		len(cfg.ReadonlySubcommands) > 0 || len(cfg.SecretPathPatterns) > 0 ||
		len(cfg.PasswordFlagPatterns) > 0
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
// recognized read-only invocation: no redirect/tee, executable in
// ReadonlyCommands, and (if the command has a configured subcommand allowlist)
// its first subcommand token is allowed.
func (r *Rule) segmentIsReadonly(seg string) bool {
	if strings.Contains(seg, ">") {
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

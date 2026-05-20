// Package auth checks credential availability for each configured provider.
//
// CheckAll iterates over the providers referenced by a Config and emits one
// Status per provider. Builtins (`github`, `github-actions`, `github-issues`,
// `jira`) are checked inline; `exec:<binary>` providers are checked by
// invoking the binary with the `auth_status` op of the scriptout protocol.
//
// The package is structured so callers (tests, the daemon, the `pg-pr auth
// status` subcommand) can inject the underlying runners (`gh`, http client,
// exec invoker) for deterministic behaviour.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/plugin/scriptout"
)

// State enumerates the four auth states the CLI surface reports.
type State string

const (
	StateOK                 State = "OK"
	StateMissing            State = "MISSING"
	StateExpired            State = "EXPIRED"
	StateInsufficientScopes State = "INSUFFICIENT_SCOPES"
)

// Status describes a single provider's auth state. JSON-friendly: the CLI
// emits a slice of Status objects when `--json` is set.
type Status struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Detail   string `json:"detail,omitempty"`
}

// IsOK reports whether the status represents a healthy provider.
func (s Status) IsOK() bool { return State(s.State) == StateOK }

// Runners bundles the side-effecting callables the checks depend on.
// Production code uses DefaultRunners(); tests pass deterministic fakes.
type Runners struct {
	// GH runs `gh <args...>` and returns combined stdout, stderr-text, and
	// the exit error (nil on exit 0). The stderr text is what gh writes to
	// stderr; `gh auth status` writes status to stderr by default.
	GH func(ctx context.Context, args ...string) (stdout, stderr string, err error)

	// HTTP performs an HTTP request and returns the response. Used by the
	// Jira check to issue a HEAD /myself probe.
	HTTP func(req *http.Request) (*http.Response, error)

	// Env looks up an environment variable. Defaults to os.Getenv.
	Env func(string) string

	// Exec invokes an exec-style provider binary with a scriptout request
	// and returns the parsed response. Defaults to a real exec subprocess.
	Exec func(ctx context.Context, binary string, req scriptout.Request) (scriptout.Response, error)
}

// DefaultRunners returns Runners that talk to the real environment.
func DefaultRunners() Runners {
	return Runners{
		GH:   defaultGH,
		HTTP: http.DefaultClient.Do,
		Env:  os.Getenv,
		Exec: defaultExec,
	}
}

// CheckAll inspects the providers referenced by cfg and returns one Status
// per provider. The slice is sorted by Provider for stable test output.
//
// Provider names follow the form already used in config:
//
//   - "github", "github-actions", "github-issues", "jira" — builtins.
//   - "exec:<binary>" — invoke <binary> via the scriptout protocol.
//
// CheckAll never returns an error from individual provider failures; those
// are reported via Status.State. It returns an error only when the inputs
// themselves are invalid (nil cfg, etc.).
func CheckAll(ctx context.Context, cfg *config.Config) ([]Status, error) {
	return CheckAllWithRunners(ctx, cfg, DefaultRunners())
}

// CheckAllWithRunners is the runner-injectable variant of CheckAll.
func CheckAllWithRunners(ctx context.Context, cfg *config.Config, runners Runners) ([]Status, error) {
	if cfg == nil {
		return nil, errors.New("auth: nil config")
	}
	runners = mergeRunners(runners)

	names := collectProviders(cfg)
	out := make([]Status, 0, len(names))
	for _, name := range names {
		out = append(out, checkOne(ctx, name, runners))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

// collectProviders returns the deduplicated set of provider names referenced
// by cfg.Repos: each repo's VCS, CICD entries, and Issues provider.
func collectProviders(cfg *config.Config) []string {
	seen := make(map[string]struct{})
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		seen[name] = struct{}{}
	}
	for _, r := range cfg.Repos {
		add(r.VCS)
		for _, c := range r.CICD {
			add(c)
		}
		add(r.Issues)
	}
	if len(seen) == 0 {
		// Sensible fallback: report on the most common builtin.
		seen["github"] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// checkOne dispatches a single provider name to its check implementation.
func checkOne(ctx context.Context, name string, r Runners) Status {
	switch {
	case name == "github", name == "github-actions", name == "github-issues":
		return checkGitHub(ctx, name, r)
	case name == "jira":
		return checkJira(ctx, r)
	case strings.HasPrefix(name, "exec:"):
		return checkExec(ctx, name, r)
	default:
		return Status{
			Provider: name,
			State:    string(StateMissing),
			Detail:   fmt.Sprintf("unknown provider %q", name),
		}
	}
}

// ----------------------------------------------------------------------
// builtin: github (and github-actions / github-issues)
// ----------------------------------------------------------------------

// checkGitHub shells out to `gh auth status` and maps the result.
//
// Exit code 0 = OK. Stderr text contains scopes; we surface them in Detail
// when present so the CLI can show the user what scopes their token has.
//
// Non-zero exit + "not logged" / "no oauth token" in output → MISSING.
// Non-zero exit + "expired" / "401" → EXPIRED.
// Anything else non-zero is reported as MISSING with the message body.
func checkGitHub(ctx context.Context, name string, r Runners) Status {
	stdout, stderr, err := r.GH(ctx, "auth", "status")
	out := stdout + stderr
	if err == nil {
		return Status{
			Provider: name,
			State:    string(StateOK),
			Detail:   summarizeGHScopes(out),
		}
	}
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "expired") || strings.Contains(low, "401"):
		return Status{Provider: name, State: string(StateExpired), Detail: firstLine(out)}
	case strings.Contains(low, "not logged") || strings.Contains(low, "no oauth token") || strings.Contains(low, "no token"):
		return Status{Provider: name, State: string(StateMissing), Detail: firstLine(out)}
	default:
		// gh may not be on PATH at all; the runner error itself is useful.
		detail := firstLine(out)
		if detail == "" {
			detail = err.Error()
		}
		return Status{Provider: name, State: string(StateMissing), Detail: detail}
	}
}

// summarizeGHScopes pulls the "Token scopes:" line out of `gh auth status`
// output, if present. Empty string when the line isn't found — keeps Detail
// terse for the common case where everything's fine.
func summarizeGHScopes(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		// gh 2.x uses "Token scopes:"; gh 1.x used "✓ Token scopes:" — match either.
		if i := strings.Index(t, "Token scopes:"); i >= 0 {
			return strings.TrimSpace(t[i:])
		}
	}
	return ""
}

// defaultGH invokes the real gh binary.
func defaultGH(ctx context.Context, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// ----------------------------------------------------------------------
// builtin: jira
// ----------------------------------------------------------------------

// checkJira looks up Jira env vars and, when all three are present, issues
// a HEAD against /rest/api/3/myself. Mapping:
//
//   - any of JIRA_API_TOKEN / JIRA_EMAIL / JIRA_BASE_URL missing → MISSING.
//   - HTTP 200 → OK.
//   - HTTP 401 / 403 → EXPIRED.
//   - HTTP transport error → MISSING with the network message.
//   - Other HTTP statuses → MISSING with the status code.
func checkJira(ctx context.Context, r Runners) Status {
	token := r.Env("JIRA_API_TOKEN")
	email := r.Env("JIRA_EMAIL")
	base := strings.TrimRight(r.Env("JIRA_BASE_URL"), "/")
	missing := []string{}
	if token == "" {
		missing = append(missing, "JIRA_API_TOKEN")
	}
	if email == "" {
		missing = append(missing, "JIRA_EMAIL")
	}
	if base == "" {
		missing = append(missing, "JIRA_BASE_URL")
	}
	if len(missing) > 0 {
		return Status{
			Provider: "jira",
			State:    string(StateMissing),
			Detail:   "missing env: " + strings.Join(missing, ", "),
		}
	}
	url := base + "/rest/api/3/myself"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return Status{Provider: "jira", State: string(StateMissing), Detail: err.Error()}
	}
	req.SetBasicAuth(email, token)
	req.Header.Set("Accept", "application/json")
	resp, err := r.HTTP(req)
	if err != nil {
		return Status{Provider: "jira", State: string(StateMissing), Detail: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		return Status{Provider: "jira", State: string(StateOK)}
	case http.StatusUnauthorized, http.StatusForbidden:
		return Status{
			Provider: "jira",
			State:    string(StateExpired),
			Detail:   fmt.Sprintf("HTTP %d from %s", resp.StatusCode, url),
		}
	default:
		return Status{
			Provider: "jira",
			State:    string(StateMissing),
			Detail:   fmt.Sprintf("HTTP %d from %s", resp.StatusCode, url),
		}
	}
}

// ----------------------------------------------------------------------
// exec providers
// ----------------------------------------------------------------------

// checkExec invokes the binary referenced by name ("exec:<binary>") with
// the auth_status op of the scriptout protocol.
//
// Mapping:
//   - Response.Error non-empty → MISSING with the error text.
//   - Response.Result decoded as scriptout.AuthStatus → use its State/Detail.
//   - Unrecognized state → MISSING (defensive).
func checkExec(ctx context.Context, name string, r Runners) Status {
	binary := strings.TrimPrefix(name, "exec:")
	if binary == "" {
		return Status{Provider: name, State: string(StateMissing), Detail: "empty binary name"}
	}
	req := scriptout.Request{Op: scriptout.OpAuthStatus, Args: json.RawMessage(`{}`)}
	resp, err := r.Exec(ctx, binary, req)
	if err != nil {
		return Status{Provider: name, State: string(StateMissing), Detail: err.Error()}
	}
	if resp.Error != "" {
		return Status{Provider: name, State: string(StateMissing), Detail: resp.Error}
	}
	// Re-marshal Result and unmarshal into our typed AuthStatus.
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return Status{Provider: name, State: string(StateMissing), Detail: "decode result: " + err.Error()}
	}
	var as scriptout.AuthStatus
	if err := json.Unmarshal(raw, &as); err != nil {
		return Status{Provider: name, State: string(StateMissing), Detail: "decode auth_status: " + err.Error()}
	}
	state := mapScriptoutState(as.State)
	return Status{Provider: name, State: string(state), Detail: as.Detail}
}

// mapScriptoutState normalizes the scriptout protocol's state into our State.
// Unknown values fall back to MISSING so we never silently report OK on junk.
func mapScriptoutState(s scriptout.AuthStatusState) State {
	switch s {
	case scriptout.AuthOK:
		return StateOK
	case scriptout.AuthMissing:
		return StateMissing
	case scriptout.AuthExpired:
		return StateExpired
	case scriptout.AuthInsufficientScopes:
		return StateInsufficientScopes
	default:
		return StateMissing
	}
}

// defaultExec invokes binary with the request JSON on stdin and decodes one
// scriptout.Response from stdout.
func defaultExec(ctx context.Context, binary string, req scriptout.Request) (scriptout.Response, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return scriptout.Response{}, fmt.Errorf("auth: marshal request: %w", err)
	}
	cmd := exec.CommandContext(ctx, binary)
	cmd.Stdin = strings.NewReader(string(payload))
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if stdout.Len() == 0 {
		if runErr != nil {
			return scriptout.Response{}, fmt.Errorf("%s: %w (stderr=%s)", binary, runErr, strings.TrimSpace(stderr.String()))
		}
		return scriptout.Response{}, fmt.Errorf("%s: no response on stdout", binary)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		return scriptout.Response{}, fmt.Errorf("%s: decode stdout: %w (stdout=%q, stderr=%q)",
			binary, err, stdout.String(), strings.TrimSpace(stderr.String()))
	}
	return resp, nil
}

// ----------------------------------------------------------------------
// internal helpers
// ----------------------------------------------------------------------

// mergeRunners returns r with nil fields replaced by their defaults. Lets
// callers (esp. tests) override only the fields they care about.
func mergeRunners(r Runners) Runners {
	d := DefaultRunners()
	if r.GH == nil {
		r.GH = d.GH
	}
	if r.HTTP == nil {
		r.HTTP = d.HTTP
	}
	if r.Env == nil {
		r.Env = d.Env
	}
	if r.Exec == nil {
		r.Exec = d.Exec
	}
	return r
}

// firstLine returns the first non-empty line of s, trimmed. Used to keep
// Detail compact when gh / network errors are multi-line.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			return t
		}
	}
	return ""
}

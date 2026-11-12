// Package broker runs the analyst's extra evidence queries. The analyst never
// calls a provider directly: it calls `agent q`, and the broker runs only a
// read-only query that the provider lists for the run's lens, appends the
// scope and window of the run, assigns the id Q-nnn, redacts and persists the
// rows, logs the call and enforces the profile's query budget.
package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/redact"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/schema"
)

// File names under the run's evidence directory.
const (
	QueryLogFile   = "query-log.jsonl"
	QueryResultDir = "query-results"
)

// defaultTimeout applies when a provider sets no timeout_seconds.
const defaultTimeout = 300 * time.Second

// RefusedError is a call the broker refused (exit 4). A refused call does not
// count against the budget.
type RefusedError struct{ Msg string }

func (e *RefusedError) Error() string { return e.Msg }

// ProviderError is a provider failure or timeout (exit 7). It counts against
// the budget.
type ProviderError struct{ Msg string }

func (e *ProviderError) Error() string { return e.Msg }

func refuse(format string, args ...any) error {
	return &RefusedError{Msg: fmt.Sprintf(format, args...)}
}

// Deps are what a call needs.
type Deps struct {
	Cfg    *config.Effective
	Ledger *ledger.Ledger
	// Now is the clock for the log entry; nil means time.Now.
	Now func() time.Time
}

// Result is a successful call: the id and the redacted rows, each of which
// carries the id in query_id.
type Result struct {
	ID   string
	Rows []map[string]any
}

// ownedFlags are flags the run or the broker supplies; an analyst-supplied one
// would change the scope or window of the run, so it is refused. The first
// three are the ones the contract names; the rest are the provider flags the
// broker itself builds from the run.
var ownedFlags = []string{
	"--scope", "--from", "--to",
	"--since", "--until", "--query", "--cwd-prefix", "--exclude-cwd-prefix", "--scope-prefix", "--layer",
}

// shellMeta are the characters the broker never lets through (the approval
// hook's allowlist applies the same rule to the whole command).
const shellMeta = "`$&|;<>(){}[]\\\"'*?!#~"

// CheckArgs refuses an argument list that holds a shell metacharacter, a
// control character or a flag the run owns.
func CheckArgs(args []string) error {
	for _, a := range args {
		for _, r := range a {
			if r < 0x20 || r == 0x7f || strings.ContainsRune(shellMeta, r) {
				return refuse("argument %q holds a shell metacharacter or control character", a)
			}
		}
		for _, f := range ownedFlags {
			if a == f || strings.HasPrefix(a, f+"=") {
				return refuse("%s comes from the run and cannot be given", f)
			}
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Flags builds the provider flags of one query from the provider's args_style,
// the run's scope and window, and the analyst's extra args (appended
// unchanged).
func Flags(cfg *config.Effective, p config.Provider, query string, sc config.Scope, from, to, scopeName string, extra []string) []string {
	var a []string
	if config.ArgsStyle(p) == builtin.ArgsStyleInventory {
		for _, pre := range sc.Prefixes {
			a = append(a, "--scope-prefix", pre)
		}
		for _, l := range sc.Layers {
			a = append(a, "--layer", l)
		}
	} else {
		a = append(a, "--query", query)
		for _, pre := range sc.Prefixes {
			a = append(a, "--cwd-prefix", pre)
		}
	}
	if n := cfg.Ccpool.NeutralDir; n != "" {
		a = append(a, "--exclude-cwd-prefix", n)
	}
	a = append(a, "--since", from, "--until", to, "--scope", scopeName)
	return append(a, extra...)
}

// utcZ normalises a stored window bound to UTC with a Z suffix.
func utcZ(s string) (string, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return "", fmt.Errorf("run window bound %q is not RFC 3339", s)
	}
	return t.UTC().Format(time.RFC3339Nano), nil
}

// Query runs one broker call for the run: args[0] is the query name and the
// rest are provider flags passed through.
func Query(ctx context.Context, d Deps, runID int64, provider string, args []string) (*Result, error) {
	if len(args) == 0 {
		return nil, refuse("no query name given")
	}
	if err := CheckArgs(append([]string{provider}, args...)); err != nil {
		return nil, err
	}
	query := args[0]
	if strings.HasPrefix(query, "-") {
		return nil, refuse("the first argument must be a query name, got %q", query)
	}
	run, err := d.Ledger.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, ledger.ErrNotFound) {
			return nil, refuse("run %d not found", runID)
		}
		return nil, err
	}
	if run.State != ledger.RunRunning && run.State != ledger.RunAwaitingAnalyst {
		return nil, refuse("run %d is %s and takes no queries", runID, run.State)
	}
	if run.EvidenceDir == "" {
		return nil, refuse("run %d has no evidence directory yet", runID)
	}
	cfg := d.Cfg
	lens, ok := cfg.Lens(run.Lens)
	if !ok {
		return nil, refuse("run %d names unknown lens %q", runID, run.Lens)
	}
	prov, ok := cfg.Provider(provider)
	if !ok {
		return nil, refuse("unknown provider %q", provider)
	}
	if !contains(prov.Queries[lens.ID], query) {
		return nil, refuse("query %q is not listed for provider %s and lens %s", query, provider, lens.ID)
	}
	if len(prov.Command) == 0 {
		return nil, refuse("provider %s has no command configured", provider)
	}
	sc, ok := cfg.Scope(run.Scope)
	if !ok {
		return nil, refuse("scope %q of the run is not configured", run.Scope)
	}
	profName := run.Profile
	if profName == "" {
		profName = lens.Profile
	}
	prof, ok := cfg.Profile(profName)
	if !ok {
		return nil, refuse("unknown profile %q", profName)
	}
	from, err := utcZ(run.WindowFrom)
	if err != nil {
		return nil, err
	}
	to, err := utcZ(run.WindowTo)
	if err != nil {
		return nil, err
	}

	// Reserve the id and the budget slot atomically: concurrent calls get
	// distinct ids and cannot jointly pass the cap.
	var n int
	err = d.Ledger.WithTx(ctx, func(t *ledger.Tx) error {
		r, err := t.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if r.QueryCount >= prof.QueriesMax {
			return refuse("query budget of %d reached for this run", prof.QueriesMax)
		}
		r.QueryCount++
		n = r.QueryCount
		return t.UpdateRun(ctx, r)
	})
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("Q-%03d", n)

	root := ""
	if len(sc.Prefixes) > 0 {
		root = sc.Prefixes[0]
	}
	flags := Flags(cfg, prov, query, sc, from, to, run.Scope, args[1:])
	rows, perr := runProvider(ctx, prov, flags, root)
	entryRows := 0
	if perr == nil {
		for i := range rows {
			rows[i]["query_id"] = id
		}
		entryRows = len(rows)
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	entry := map[string]any{
		"schema_version": 1, "id": id, "provider": provider, "args": args,
		"scope": run.Scope, "rows": entryRows, "at": now().UTC().Format(time.RFC3339),
	}
	if perr == nil {
		if err := persist(run.EvidenceDir, id, rows); err != nil {
			return nil, err
		}
	}
	if err := appendLog(run.EvidenceDir, entry); err != nil {
		return nil, err
	}
	if perr != nil {
		return nil, perr
	}
	return &Result{ID: id, Rows: rows}, nil
}

func runProvider(ctx context.Context, p config.Provider, flags []string, scopeRoot string) ([]map[string]any, error) {
	timeout := time.Duration(p.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, p.Command[0], append(append([]string{}, p.Command[1:]...), flags...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return nil, &ProviderError{fmt.Sprintf("provider %s timed out after %s", p.Name, timeout)}
		}
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 500 {
			msg = msg[:500]
		}
		msg, _ = redact.Text(msg, scopeRoot)
		return nil, &ProviderError{fmt.Sprintf("provider %s: %v: %s", p.Name, err, msg)}
	}
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		return nil, &ProviderError{fmt.Sprintf("provider %s output is not a JSON array of objects: %v", p.Name, err)}
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	for i, r := range rows {
		v, _ := redact.Value(r, scopeRoot)
		row := v.(map[string]any)
		// The validator resolves a Q addend against a row's top-level
		// numeric keys, so a provider row's metrics are lifted to the top
		// level (never over an existing key).
		if m, ok := row["metrics"].(map[string]any); ok {
			for k, x := range m {
				if _, taken := row[k]; !taken {
					row[k] = x
				}
			}
		}
		rows[i] = row
	}
	return rows, nil
}

func persist(dir, id string, rows []map[string]any) error {
	rd := filepath.Join(dir, QueryResultDir)
	if err := os.MkdirAll(rd, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	p := filepath.Join(rd, id+".json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

func appendLog(dir string, entry map[string]any) error {
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if err := schema.Validate("query-log-entry", b); err != nil {
		return fmt.Errorf("query log entry: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, QueryLogFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(append(b, '\n'))
	return err
}

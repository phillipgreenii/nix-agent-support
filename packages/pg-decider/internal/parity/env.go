package parity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Environment variables EnvFromProcess reads, each an absolute path to a built
// binary.
const (
	envPgDeskBin      = "PG_DECIDER_PARITY_PG_DESK_BIN"
	envPgConnectorBin = "PG_DECIDER_PARITY_PG_CONNECTOR_BIN"
	envPgDeciderBin   = "PG_DECIDER_PARITY_PG_DECIDER_BIN"
)

// Env names the three built binaries the runners exec and the directory all
// run state lives under.
type Env struct {
	PgDeskBin, PgConnectorBin, PgDeciderBin string // absolute paths to the built binaries
	TempDir                                 string // fresh per run; all state lives under it
}

// EnvFromProcess reads the three binary paths from the process environment
// (PG_DECIDER_PARITY_PG_DESK_BIN, PG_DECIDER_PARITY_PG_CONNECTOR_BIN and
// PG_DECIDER_PARITY_PG_DECIDER_BIN); it is false when any is unset or empty.
// TempDir is left empty for the caller to set (typically os.MkdirTemp), which
// keeps creating and removing it together. A runner given an empty TempDir
// makes, and removes, one of its own.
//
// PgConnectorBin is never run. It identifies the REAL pg-connector, so the
// harness can prove no child process can reach it: the generated fake is the
// only pg-connector a child finds.
func EnvFromProcess() (Env, bool) {
	e := Env{
		PgDeskBin:      os.Getenv(envPgDeskBin),
		PgConnectorBin: os.Getenv(envPgConnectorBin),
		PgDeciderBin:   os.Getenv(envPgDeciderBin),
	}
	if e.PgDeskBin == "" || e.PgConnectorBin == "" || e.PgDeciderBin == "" {
		return Env{}, false
	}
	return e, true
}

// validate checks the binaries are absolute paths of executable files and the
// TempDir, when set, is absolute.
func (e Env) validate() error {
	for _, b := range []struct{ name, path string }{
		{"PgDeskBin", e.PgDeskBin}, {"PgConnectorBin", e.PgConnectorBin}, {"PgDeciderBin", e.PgDeciderBin},
	} {
		if !filepath.IsAbs(b.path) {
			return fmt.Errorf("parity: Env.%s %q is not an absolute path", b.name, b.path)
		}
		if !isExecutable(b.path) {
			return fmt.Errorf("parity: Env.%s %q is not an executable file", b.name, b.path)
		}
	}
	if e.TempDir != "" && !filepath.IsAbs(e.TempDir) {
		return fmt.Errorf("parity: Env.TempDir %q is not an absolute path", e.TempDir)
	}
	return nil
}

func isExecutable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0
}

// sandbox is one hermetic run: its own state directory, config, HOME, runtime
// directory and a PATH holding only pg-desk and the fixture-driven fake
// pg-connector.
type sandbox struct {
	fx *Fixture

	dir        string // the run's root; everything below lives under it
	binDir     string // the child PATH: pg-desk and the fake pg-connector
	stateHome  string // XDG_STATE_HOME; the store is <stateHome>/pg-desk/store.db
	configPath string // PG_DESK_CONFIG
	callLog    string // every call the fake pg-connector received
	workDir    string // the children's working directory

	// deskExec and deciderExec are the binaries actually run: the unwrapped
	// binaries when env names nix wrappers (see unwrapped).
	deskExec, deciderExec string

	env []string // the complete child environment; nothing is inherited
}

// newSandbox builds the run's directories, config, PATH and fake connector.
// side labels the run directory ("old" or "new"); syncMode is the config's
// sync.mode. The returned cleanup removes the run directory only when the
// runner made the base temp dir itself (Env.TempDir empty).
func newSandbox(env Env, fx *Fixture, side, syncMode string) (*sandbox, func(), error) {
	noop := func() {}
	if err := env.validate(); err != nil {
		return nil, noop, err
	}
	cleanup := noop
	base := env.TempDir
	if base == "" {
		d, err := os.MkdirTemp("", "pg-decider-parity-*")
		if err != nil {
			return nil, noop, fmt.Errorf("parity: make temp dir: %w", err)
		}
		base = d
		cleanup = func() { _ = os.RemoveAll(d) }
	} else if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, noop, fmt.Errorf("parity: make Env.TempDir: %w", err)
	}
	fail := func(err error) (*sandbox, func(), error) {
		cleanup()
		return nil, noop, err
	}

	run, err := os.MkdirTemp(base, side+"-"+fx.Name+"-")
	if err != nil {
		return fail(fmt.Errorf("parity: make run dir: %w", err))
	}
	// Resolve symlinks (macOS keeps /var behind /private/var) once, so every
	// later comparison with a path a binary reports is exact.
	if run, err = filepath.EvalSymlinks(run); err != nil {
		return fail(fmt.Errorf("parity: resolve run dir: %w", err))
	}

	sb := &sandbox{
		fx:         fx,
		dir:        run,
		binDir:     filepath.Join(run, "bin"),
		stateHome:  filepath.Join(run, "state"),
		configPath: filepath.Join(run, "config", "pg-desk.yaml"),
		callLog:    filepath.Join(run, "connector-calls.log"),
		workDir:    filepath.Join(run, "work"),
	}
	runtimeDir := filepath.Join(run, "runtime")
	for _, d := range []string{
		sb.binDir, sb.stateHome, filepath.Dir(sb.configPath), sb.workDir,
		filepath.Join(run, "home"), filepath.Join(run, "tmp"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fail(fmt.Errorf("parity: make %s: %w", d, err))
		}
	}
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return fail(fmt.Errorf("parity: make %s: %w", runtimeDir, err))
	}

	// The harness never touches a real store or tracker: refuse to go on when
	// the store would land outside the run directory.
	if err := checkStoreInside(run, sb.stateHome); err != nil {
		return fail(err)
	}

	realConnectorDir := filepath.Dir(env.PgConnectorBin)
	if sb.deskExec, err = unwrapped(env.PgDeskBin, realConnectorDir); err != nil {
		return fail(err)
	}
	if sb.deciderExec, err = unwrapped(env.PgDeciderBin, realConnectorDir); err != nil {
		return fail(err)
	}
	if sb.binDir == realConnectorDir {
		return fail(fmt.Errorf("parity: the real pg-connector directory %s is the child PATH", realConnectorDir))
	}

	// PATH holds pg-desk (pg-decider execs it by name) and the fake
	// pg-connector (pg-desk execs it by name) and nothing else.
	if err := os.Symlink(sb.deskExec, filepath.Join(sb.binDir, "pg-desk")); err != nil {
		return fail(fmt.Errorf("parity: link pg-desk: %w", err))
	}
	fake := filepath.Join(sb.binDir, "pg-connector")
	if err := os.WriteFile(fake, []byte(fakeConnectorScript(fx, sb.callLog)), 0o755); err != nil {
		return fail(fmt.Errorf("parity: write fake pg-connector: %w", err))
	}
	if err := os.WriteFile(sb.callLog, nil, 0o644); err != nil {
		return fail(fmt.Errorf("parity: make call log: %w", err))
	}
	if err := os.WriteFile(sb.configPath, []byte(configYAML(syncMode)), 0o644); err != nil {
		return fail(fmt.Errorf("parity: write config: %w", err))
	}

	sb.env = []string{
		"PATH=" + sb.binDir,
		"HOME=" + filepath.Join(run, "home"),
		"TMPDIR=" + filepath.Join(run, "tmp"),
		"XDG_STATE_HOME=" + sb.stateHome,
		"XDG_CONFIG_HOME=" + filepath.Join(run, "config"),
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"PG_DESK_CONFIG=" + sb.configPath,
	}
	return sb, cleanup, nil
}

// configYAML is the synthetic pg-desk config both sides run under: one
// repository, the operator as the acting identity, one teammate.
func configYAML(syncMode string) string {
	return fmt.Sprintf(`self_login: phillipgreenii
team_members:
  - teammate
repos:
  - remote: %s
actor: parity-harness
sync:
  mode: %s
`, FixtureRepo, syncMode)
}

// storePath is where pg-desk keeps this run's store.
func (sb *sandbox) storePath() string {
	return filepath.Join(sb.stateHome, "pg-desk", "store.db")
}

// checkStoreInside refuses to start when the store pg-desk would resolve from
// stateHome (XDG_STATE_HOME) is not under tempDir. An unset or relative
// stateHome is refused too: pg-desk would then fall back to the real home.
func checkStoreInside(tempDir, stateHome string) error {
	if stateHome == "" || !filepath.IsAbs(stateHome) {
		return fmt.Errorf("parity: XDG_STATE_HOME %q is not an absolute path; pg-desk would use the real state directory", stateHome)
	}
	if !filepath.IsAbs(tempDir) {
		return fmt.Errorf("parity: temp dir %q is not an absolute path", tempDir)
	}
	store := resolvePath(filepath.Join(stateHome, "pg-desk", "store.db"))
	root := resolvePath(tempDir)
	rel, err := filepath.Rel(root, store)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("parity: refusing to run: the store %s is not under the temp dir %s", store, root)
	}
	return nil
}

// resolvePath cleans p and resolves symlinks through its deepest existing
// ancestor, so a path that does not exist yet still compares correctly.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolvePath(parent), filepath.Base(p))
}

// unwrapped returns the binary to run for bin. Nix installs pg-desk and
// pg-decider behind a wrapper that PREFIXES the real pg-connector onto PATH,
// which would shadow the fake: when `.<name>-wrapped` sits beside bin, that is
// the program to run and PATH stays what the harness set. A script that
// mentions the real pg-connector's directory and has no unwrapped sibling is
// refused, because the real connector would be reachable.
func unwrapped(bin, realConnectorDir string) (string, error) {
	w := filepath.Join(filepath.Dir(bin), "."+filepath.Base(bin)+"-wrapped")
	if isExecutable(w) {
		return w, nil
	}
	f, err := os.Open(bin)
	if err != nil {
		return "", fmt.Errorf("parity: read %s: %w", bin, err)
	}
	defer func() { _ = f.Close() }()
	// A wrapper is a small script; a compiled binary is not read past its head.
	head := make([]byte, 16<<10)
	n, err := f.Read(head)
	if err != nil && n == 0 {
		return "", fmt.Errorf("parity: read %s: %w", bin, err)
	}
	head = head[:n]
	if bytes.HasPrefix(head, []byte("#!")) && bytes.Contains(head, []byte(realConnectorDir)) {
		return "", fmt.Errorf("parity: %s is a wrapper that puts the real pg-connector (%s) on PATH and has no unwrapped %s beside it; refusing to run", bin, realConnectorDir, w)
	}
	return bin, nil
}

// execResult is one child process's outcome.
type execResult struct {
	Stdout, Stderr string
	Code           int
}

// run executes bin with args in the sandbox. The error is non-nil only when the
// process could not be started or the context ended; a non-zero exit is a
// result for the caller to judge.
func (sb *sandbox) run(ctx context.Context, bin string, args ...string) (execResult, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = sb.env
	cmd.Dir = sb.workDir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := execResult{Stdout: out.String(), Stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
		return res, nil
	case errors.As(err, &ee) && ctx.Err() == nil:
		res.Code = ee.ExitCode()
		return res, nil
	}
	return res, fmt.Errorf("parity: run %s %s: %w", filepath.Base(bin), strings.Join(args, " "), err)
}

// step runs bin and requires exit 0. A refusal by the binary is wrapped in
// ErrUnsupported, because the scenario then cannot be loaded through it; a
// failure to run at all is a plain error.
func (sb *sandbox) step(ctx context.Context, bin string, args ...string) (execResult, error) {
	res, err := sb.run(ctx, bin, args...)
	if err != nil {
		return res, err
	}
	if res.Code != 0 {
		return res, fmt.Errorf("%w: %s %s exited %d: %s", ErrUnsupported, filepath.Base(bin), strings.Join(args, " "), res.Code,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return res, nil
}

// verifyStore asks the real pg-desk where its store is and requires the answer
// to be the sandbox's, then re-checks it is under the run directory.
func (sb *sandbox) verifyStore(ctx context.Context) error {
	res, err := sb.run(ctx, sb.deskExec, "status")
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("parity: pg-desk status exited %d: %s", res.Code, strings.TrimSpace(res.Stderr))
	}
	var reported string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if p, ok := strings.CutPrefix(line, "store: "); ok {
			reported = strings.TrimSpace(p)
		}
	}
	if reported != sb.storePath() {
		return fmt.Errorf("parity: refusing to run: pg-desk resolves its store to %q, not the sandbox's %q", reported, sb.storePath())
	}
	return checkStoreInside(sb.dir, filepath.Dir(filepath.Dir(reported)))
}

// assertConnectorUsed proves the fake pg-connector, not some other one, served
// the hydration: it must have been asked for every evaluated entity.
func (sb *sandbox) assertConnectorUsed(entities []string) error {
	log, err := os.ReadFile(sb.callLog)
	if err != nil {
		return fmt.Errorf("parity: read connector call log: %w", err)
	}
	lines := strings.Split(string(log), "\n")
	for _, e := range entities {
		if !contains(lines, "pr show "+e) && !contains(lines, "pr show "+e+" --fresh") {
			return fmt.Errorf("parity: the fake pg-connector was never asked for `pr show %s`; pg-desk reached a different pg-connector", e)
		}
	}
	return nil
}

// Package prepare builds a scratch directory: the store copy, the configs, the
// pinned tool links and shims, the manifest and (phase A) the warm-up.
package prepare

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/runner"
	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
	"github.com/phillipgreenii/pg-desk-shadow/internal/scratch"
	"github.com/phillipgreenii/pg-desk-shadow/internal/sqlite"
	"github.com/phillipgreenii/pg-desk-shadow/internal/warmup"
)

// Options configures prepare.
type Options struct {
	Root        string
	Phase       string
	NoSeed      bool
	Queries     []string
	BDMode      string // passthrough | hermetic
	LiveStore   string
	DeskConfig  string // live pg-desk config
	PRConfig    string // live pg-pr config
	Home        string
	Self        string // this binary (the shims re-enter it)
	SandboxExec string
	SweepMaxAge string
	Live        scratch.LiveSources
	Log         io.Writer
	LookPath    func(string) (string, error)
}

// Defaults fills unset options from the environment.
func (o *Options) Defaults() error {
	if o.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		o.Home = h
	}
	if o.Phase == "" {
		o.Phase = "A"
	}
	if len(o.Queries) == 0 {
		o.Queries = []string{"mine", "team"}
	}
	if o.BDMode == "" {
		o.BDMode = "passthrough"
	}
	if o.LiveStore == "" {
		o.LiveStore = filepath.Join(o.Home, ".local", "state", "pg-desk", "store.db")
	}
	if o.DeskConfig == "" {
		o.DeskConfig = filepath.Join(o.Home, ".config", "pg-desk", "config.yaml")
	}
	if o.PRConfig == "" {
		o.PRConfig = filepath.Join(o.Home, ".config", "pg-pr", "config.yaml")
	}
	if o.SandboxExec == "" {
		o.SandboxExec = safety.DefaultSandboxExec
	}
	if o.SweepMaxAge == "" {
		o.SweepMaxAge = "8760h"
	}
	if o.Live.RouterEvents == "" {
		st := filepath.Join(o.Home, ".local", "state")
		o.Live = scratch.LiveSources{
			RouterEvents:    filepath.Join(st, "pg-router", "events.jsonl"),
			RouterQueue:     filepath.Join(st, "pg-router", "queue.jsonl"),
			RunRecord:       filepath.Join(st, "pg-desk", "run-record.log"),
			ConnectorEvents: filepath.Join(st, "pg-connector-pr-github", "events.jsonl"),
		}
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Self == "" {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		o.Self = self
	}
	switch o.BDMode {
	case "passthrough", "hermetic":
	default:
		return fmt.Errorf("--bd-mode must be passthrough or hermetic, got %q", o.BDMode)
	}
	return nil
}

func (o Options) logf(f string, a ...any) { _, _ = fmt.Fprintf(o.Log, f+"\n", a...) }

// Prepare runs every step; it refuses an existing prepared directory.
func Prepare(ctx context.Context, o Options) (scratch.Layout, scratch.Manifest, error) {
	var m scratch.Manifest
	if err := o.Defaults(); err != nil {
		return scratch.Layout{}, m, err
	}
	abs, err := filepath.Abs(o.Root)
	if err != nil {
		return scratch.Layout{}, m, err
	}
	root := safety.Resolve(abs)
	l := scratch.Layout{Root: root}
	if _, err := os.Stat(l.Manifest()); err == nil {
		return l, m, fmt.Errorf("prepare: %s is already prepared (run.json exists); use a new directory", root)
	}
	for _, live := range scratch.LiveRoots(o.Home) {
		if safety.Under(root, live) || safety.Under(live, root) {
			return l, m, fmt.Errorf("prepare: the scratch directory %s is (or contains) the live state directory %s", root, live)
		}
	}
	if root == safety.Resolve(o.Home) {
		return l, m, fmt.Errorf("prepare: the scratch directory must not be the home directory")
	}
	if err := l.MkdirAll(); err != nil {
		return l, m, err
	}

	// Configs.
	liveDesk, err := os.ReadFile(o.DeskConfig)
	if err != nil {
		return l, m, fmt.Errorf("prepare: read the live pg-desk config: %w", err)
	}
	livePR, err := os.ReadFile(o.PRConfig)
	if err != nil {
		return l, m, fmt.Errorf("prepare: read the live pg-pr config: %w", err)
	}
	deskYAML, info, err := scratch.DeriveDeskConfig(liveDesk, scratch.DeskParams{Queries: o.Queries, SweepMaxAge: o.SweepMaxAge, HermeticBD: o.BDMode == "hermetic"})
	if err != nil {
		return l, m, err
	}
	prYAML, err := scratch.DerivePRConfig(livePR)
	if err != nil {
		return l, m, err
	}
	if err := os.WriteFile(l.DeskConfig(), deskYAML, 0o600); err != nil {
		return l, m, err
	}
	if err := os.WriteFile(l.PRConfig(), prYAML, 0o600); err != nil {
		return l, m, err
	}

	// Tools.
	var tools []scratch.Tool
	reals := map[string]string{}
	for _, name := range []string{"pg-desk", "pg-connector", "pg-connector-pr-github", "pg-connector-ci-github-actions", "pg-connector-issue-beads", "pg-router-source-pg-desk"} {
		t, err := scratch.Resolve(name, o.LookPath)
		if err != nil {
			if name == "pg-connector-ci-github-actions" || name == "pg-router-source-pg-desk" {
				o.logf("prepare: optional tool %s not found (%v)", name, err)
				continue
			}
			return l, m, err
		}
		tools = append(tools, t)
		reals[name] = t.Path
	}
	ghReal, err := scratch.Resolve("gh", o.LookPath)
	if err != nil {
		return l, m, err
	}
	bdReal, err := scratch.Resolve("bd", o.LookPath)
	if err != nil {
		return l, m, err
	}
	// gh and bd are never unwrapped: the shim must reach the machine bd as is.
	ghPath, _ := o.LookPath("gh")
	bdPath, _ := o.LookPath("bd")
	_ = ghReal
	_ = bdReal
	reals["gh"], reals["bd"] = ghPath, bdPath
	shims := map[string]string{
		"gh": scratch.ShimScript(o.Self, "gh", ghPath, l.ShimLog("gh"), false),
		"bd": scratch.ShimScript(o.Self, "bd", bdPath, l.ShimLog("bd"), o.BDMode == "hermetic"),
	}
	if err := l.LinkTools(tools, shims); err != nil {
		return l, m, err
	}

	m = scratch.Manifest{
		Phase: o.Phase, CreatedAt: schema.Format(time.Now()), Seeded: false, BDMode: o.BDMode, BeadsDir: info.BeadsDir,
		Queries: o.Queries, Consumer: "shadow-compare", MaxPerPoll: info.MaxPerPoll, SweepMaxAge: o.SweepMaxAge, ReconcileAge: "30m",
		LiveStore: o.LiveStore, Home: o.Home, SandboxExec: o.SandboxExec, Tools: map[string]string{}, Reals: reals,
		Builds: map[string]string{}, Scrub: []string{info.SelfLogin, info.Remote}, Live: o.Live,
	}
	if o.NoSeed {
		m.SweepMaxAge = "default"
	}
	for _, t := range tools {
		m.Tools[t.Name] = filepath.Join(l.BinDir(), t.Name)
	}
	m.Tools["gh"], m.Tools["bd"] = filepath.Join(l.BinDir(), "gh"), filepath.Join(l.BinDir(), "bd")
	m.Builds["pg-desk"] = scratch.Version(ctx, filepath.Join(l.BinDir(), "pg-desk"))
	m.Builds["pg-connector"] = scratch.Version(ctx, filepath.Join(l.BinDir(), "pg-connector"))
	if live, err := o.LookPath("pg-desk"); err == nil {
		m.Builds["live-pg-desk"] = scratch.Version(ctx, live)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return l, m, err
	}
	if err := os.WriteFile(l.HMACKey(), []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return l, m, err
	}

	pol := l.Policy(m)
	run := &runner.Runner{Policy: pol, Env: safety.ChildEnv(pol, "/usr/bin:/bin"), SandboxExec: o.SandboxExec, WorkDir: l.WorkDir()}
	if err := safety.SelfTest(ctx, o.SandboxExec, pol.Scratch, l.TmpDir()); err != nil {
		return l, m, err
	}
	o.logf("prepare: sandbox self-test passed")

	// Store: read-only snapshot of the live store, then the cutover.
	if err := sqlite.Backup(ctx, o.LiveStore, l.StoreDB()); err != nil {
		return l, m, fmt.Errorf("prepare: backup the live store: %w", err)
	}
	m.CreatedAt = schema.Format(time.Now()) // T0: the moment of the copy
	db := sqlite.DB{Path: l.StoreDB()}
	if ok, err := db.Query(ctx, "PRAGMA integrity_check"); err != nil || len(ok) != 1 || sqlite.Str(ok[0]["integrity_check"]) != "ok" {
		return l, m, fmt.Errorf("prepare: the store copy failed its integrity check: %v", err)
	}
	ver, err := db.Int(ctx, "PRAGMA user_version")
	if err != nil {
		return l, m, err
	}
	if ver < 2 {
		res, err := run.Run(ctx, 2*time.Minute, filepath.Join(l.BinDir(), "pg-desk"), "migrate", "--cutover")
		if err != nil || res.Exit != 0 {
			return l, m, fmt.Errorf("prepare: migrate --cutover: exit %d: %v %s", res.Exit, err, strings.TrimSpace(res.Stderr))
		}
		o.logf("prepare: migrate --cutover applied to the copy")
	}
	n, _ := db.Int(ctx, "SELECT count(*) FROM entity WHERE entity_type='pr'")
	o.logf("prepare: store copy holds %d pr rows", n)

	if !o.NoSeed {
		if err := seed(ctx, o, l, &m, run, db); err != nil {
			return l, m, err
		}
	}
	if err := l.Save(m); err != nil {
		return l, m, err
	}
	return l, m, nil
}

func seed(ctx context.Context, o Options, l scratch.Layout, m *scratch.Manifest, run *runner.Runner, db sqlite.DB) error {
	var listings []warmup.Listing
	for _, q := range o.Queries {
		res, err := run.Run(ctx, 5*time.Minute, filepath.Join(l.BinDir(), "pg-connector"), "pr", "list", "--query", q, "--fingerprints", "--output", "json")
		if err != nil {
			return fmt.Errorf("prepare: warm-up list %q: %w", q, err)
		}
		if res.Denied {
			return fmt.Errorf("prepare: warm-up list %q hit a sandbox denial", q)
		}
		if res.Exit != 0 && res.Exit != 2 {
			return fmt.Errorf("prepare: warm-up list %q exited %d: %s", q, res.Exit, firstLine(res.Stderr+res.Stdout))
		}
		lst, err := warmup.ParseListing(q, []byte(res.Stdout))
		if err != nil {
			return err
		}
		o.logf("prepare: warm-up query %q listed %d entities (exit %d)", q, len(lst.Entities), res.Exit)
		listings = append(listings, lst)
	}
	primed := warmup.Prime(listings)
	now := time.Now().UTC()
	active, err := warmup.Seed(ctx, db, primed, now)
	if err != nil {
		return err
	}
	m.Seeded, m.SeededAt = true, schema.Format(now)
	ids, err := db.Query(ctx, "SELECT entity_id FROM entity WHERE entity_type='pr' AND active=1")
	if err != nil {
		return err
	}
	var seeded []string
	for _, r := range ids {
		seeded = append(seeded, sqlite.Str(r["entity_id"]))
	}
	sort.Strings(seeded)
	b, _ := json.Marshal(map[string]any{"ids": seeded, "listed": len(primed.Listed), "primed": len(primed.FP), "stale_skipped": len(primed.Stale), "active_after": active})
	o.logf("prepare: seeded: %d listed, %d primed with a fingerprint, %d stale skipped, %d active rows remain", len(primed.Listed), len(primed.FP), len(primed.Stale), active)
	return os.WriteFile(l.SeedFile(), append(b, '\n'), 0o600)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

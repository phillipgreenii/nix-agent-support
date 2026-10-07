// Package scratch builds and describes the scratch directory every shadow
// file lives in.
package scratch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
)

// Layout resolves every path under the scratch root.
type Layout struct{ Root string }

func (l Layout) j(p ...string) string { return filepath.Join(append([]string{l.Root}, p...)...) }

func (l Layout) StateHome() string       { return l.j("state") }
func (l Layout) RuntimeDir() string      { return l.j("run") }
func (l Layout) TmpDir() string          { return l.j("tmp") }
func (l Layout) BinDir() string          { return l.j("bin") }
func (l Layout) WorkDir() string         { return l.j("work") }
func (l Layout) ConfigDir() string       { return l.j("config") }
func (l Layout) DeskConfig() string      { return l.j("config", "pg-desk.yaml") }
func (l Layout) PRConfig() string        { return l.j("config", "pg-pr.yaml") }
func (l Layout) StoreDB() string         { return l.j("state", "pg-desk", "store.db") }
func (l Layout) LogsDir() string         { return l.j("logs") }
func (l Layout) ShimLog(t string) string { return l.j("logs", "shim-"+t+".log") }
func (l Layout) SandboxLog() string      { return l.j("logs", "sandbox-denials.log") }
func (l Layout) CollectorDir() string    { return l.j("collector") }
func (l Layout) TicksFile() string       { return l.j("collector", "ticks.jsonl") }
func (l Layout) StateFile() string       { return l.j("collector", "state.json") }
func (l Layout) ProjectionFile() string  { return l.j("collector", "projection.json") }
func (l Layout) CollectorLog() string    { return l.j("collector", "collector.log") }
func (l Layout) LockFile() string        { return l.j("collector", "collector.lock") }
func (l Layout) LiveDir() string         { return l.j("collector", "live") }
func (l Layout) ReportsDir() string      { return l.j("reports") }
func (l Layout) HMACKey() string         { return l.j("hmac.key") }
func (l Layout) Manifest() string        { return l.j("run.json") }
func (l Layout) SeedFile() string        { return l.j("seed.json") }

// ConnectorLog is the scratch pg-connector-pr-github event log (the backend
// writes it under $XDG_STATE_HOME).
func (l Layout) ConnectorLog() string {
	return l.j("state", "pg-connector-pr-github", "events.jsonl")
}

// Manifest records how the scratch directory was prepared.
type Manifest struct {
	Schema    string `json:"schema"`
	Phase     string `json:"phase"`
	CreatedAt string `json:"created_at"` // T0
	Seeded    bool   `json:"seeded"`
	SeededAt  string `json:"seeded_at,omitempty"`
	// WarmupSimplification states the report sentence about the seeding.
	BDMode       string   `json:"bd_mode"` // passthrough | hermetic
	BeadsDir     string   `json:"beads_dir,omitempty"`
	Queries      []string `json:"queries"`
	Consumer     string   `json:"consumer"`
	MaxPerPoll   int      `json:"hydration_max_per_poll"`
	SweepMaxAge  string   `json:"sweep_max_age"`
	ReconcileAge string   `json:"sweep_reconcile_age"`
	LiveStore    string   `json:"live_store"`
	Home         string   `json:"home"`
	SandboxExec  string   `json:"sandbox_exec"`
	// Tools maps a tool name to the pinned absolute path in the scratch bin.
	Tools  map[string]string `json:"tools"`
	Reals  map[string]string `json:"reals"`
	Builds map[string]string `json:"builds"`
	// Scrub lists literal strings (login, repo slug) that must never appear in
	// an error message that leaves the scratch directory.
	Scrub []string `json:"scrub"`
	// Live source paths.
	Live LiveSources `json:"live"`
}

// LiveSources names the live logs the collector tails and the live connector
// log used by the budget guard.
type LiveSources struct {
	RouterEvents    string `json:"router_events"`
	RouterQueue     string `json:"router_queue"`
	RunRecord       string `json:"run_record"`
	ConnectorEvents string `json:"connector_events"`
}

// ManifestSchema is the manifest version id.
const ManifestSchema = "pg-desk-shadow.run/v1"

// Policy derives the safety policy for this scratch directory.
func (l Layout) Policy(m Manifest) safety.Policy {
	root := safety.Resolve(l.Root)
	return safety.Policy{
		Scratch:    root,
		StateHome:  l.StateHome(),
		RuntimeDir: l.RuntimeDir(),
		TmpDir:     l.TmpDir(),
		BinDir:     l.BinDir(),
		DeskConfig: l.DeskConfig(),
		PRConfig:   l.PRConfig(),
		BeadsDir:   m.BeadsDir,
		Home:       m.Home,
		LiveRoots:  LiveRoots(m.Home),
	}
}

// LiveRoots are the state directories the run must never use.
func LiveRoots(home string) []string {
	if home == "" {
		return nil
	}
	return []string{filepath.Join(home, ".local", "state")}
}

// Save writes the manifest.
func (l Layout) Save(m Manifest) error {
	m.Schema = ManifestSchema
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.Manifest(), append(b, '\n'), 0o600)
}

// Load reads the manifest.
func (l Layout) Load() (Manifest, error) {
	b, err := os.ReadFile(l.Manifest())
	if err != nil {
		return Manifest{}, fmt.Errorf("scratch: read manifest (was prepare run?): %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("scratch: manifest: %w", err)
	}
	return m, nil
}

// T0 parses the manifest creation time.
func (m Manifest) T0() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, m.CreatedAt)
	return t
}

// MkdirAll creates the scratch tree.
func (l Layout) MkdirAll() error {
	for _, d := range []string{
		l.Root, l.StateHome(), filepath.Dir(l.StoreDB()), l.TmpDir(), l.BinDir(), l.WorkDir(), l.ConfigDir(),
		l.LogsDir(), l.CollectorDir(), l.LiveDir(), l.ReportsDir(), filepath.Dir(l.ConnectorLog()),
	} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return os.MkdirAll(l.RuntimeDir(), 0o700)
}

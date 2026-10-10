package scratch

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/phillipgreenii/pg-desk-shadow/internal/safety"
)

// DeskParams are the values the scratch pg-desk config overrides.
type DeskParams struct {
	Queries     []string
	SweepMaxAge string
	HermeticBD  bool
	// HermeticDir is the scratch beads workspace written as beads_dir in the
	// hermetic bd mode.
	HermeticDir  string
	ReconcileAge string
}

// DeskInfo is what the derivation learned from the live config.
type DeskInfo struct {
	SelfLogin string
	Remote    string
	BeadsDir  string
	// MaxPerPoll is hydration.max_per_poll (the pg-desk default 50 when unset).
	MaxPerPoll int
}

// DeriveDeskConfig turns a live pg-desk config into the scratch one: sync off,
// no ticket patterns, no Jira, the watched PR queries, a huge sweep.max_age.
// self_login and repos[0].remote are required; repos[0].beads_dir is KEPT
// (read-only use) unless the bd mode is hermetic.
func DeriveDeskConfig(live []byte, p DeskParams) ([]byte, DeskInfo, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(live, &doc); err != nil {
		return nil, DeskInfo{}, fmt.Errorf("pg-desk config: %w", err)
	}
	if doc == nil {
		return nil, DeskInfo{}, fmt.Errorf("pg-desk config is empty")
	}
	var info DeskInfo
	info.SelfLogin, _ = doc["self_login"].(string)
	if info.SelfLogin == "" {
		return nil, DeskInfo{}, fmt.Errorf("pg-desk config: self_login is required")
	}
	repos, _ := doc["repos"].([]any)
	if len(repos) == 0 {
		return nil, DeskInfo{}, fmt.Errorf("pg-desk config: repos[0] is required")
	}
	repo, _ := repos[0].(map[string]any)
	info.Remote, _ = repo["remote"].(string)
	if info.Remote == "" {
		return nil, DeskInfo{}, fmt.Errorf("pg-desk config: repos[0].remote is required")
	}
	info.BeadsDir, _ = repo["beads_dir"].(string)
	if p.HermeticBD {
		if p.HermeticDir == "" {
			return nil, DeskInfo{}, fmt.Errorf("pg-desk config: the hermetic bd mode needs a scratch beads workspace directory")
		}
		repo["beads_dir"] = p.HermeticDir
		info.BeadsDir = p.HermeticDir
	} else if info.BeadsDir == "" {
		return nil, DeskInfo{}, fmt.Errorf("pg-desk config: repos[0].beads_dir is required unless the bd mode is hermetic (every hydration would degrade)")
	}
	doc["repos"] = []any{repo}
	info.MaxPerPoll = 50
	if h, ok := doc["hydration"].(map[string]any); ok {
		if n, ok := h["max_per_poll"].(int); ok && n > 0 {
			info.MaxPerPoll = n
		}
	}

	sync, _ := doc["sync"].(map[string]any)
	if sync == nil {
		sync = map[string]any{}
	}
	sync["mode"] = "off"
	doc["sync"] = sync
	delete(doc, "ticket_patterns")
	delete(doc, "jira")
	delete(doc, "serve")

	watch, _ := doc["watch"].(map[string]any)
	if watch == nil {
		watch = map[string]any{}
	}
	qs := make([]any, len(p.Queries))
	for i, q := range p.Queries {
		qs[i] = q
	}
	pr, _ := watch["pr"].(map[string]any)
	if pr == nil {
		pr = map[string]any{}
	}
	pr["queries"] = qs
	watch["pr"] = pr
	delete(watch, "issue")
	delete(watch, "thread")
	doc["watch"] = watch

	sweep, _ := doc["sweep"].(map[string]any)
	if sweep == nil {
		sweep = map[string]any{}
	}
	if p.SweepMaxAge != "" {
		sweep["max_age"] = p.SweepMaxAge
	}
	if p.ReconcileAge != "" {
		sweep["reconcile_age"] = p.ReconcileAge
	}
	doc["sweep"] = sweep

	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, DeskInfo{}, err
	}
	return out, info, nil
}

// DerivePRConfig turns a live pg-pr (pg-connector) config into the scratch one.
// It removes the Jira backend (a Jira string in a list, a map key naming it, and
// a {name, command} instance whose name or binary names it), and pins every
// --beads-dir word of a registered command to beadsDir, the policy's beads
// directory. The flag beats BEADS_DIR, so leaving a live tracker path in place
// would read the live trackers instead of the read-only one (pg2-ghmw0). The
// result is a standalone file, so the live config (a read-only nix-store
// symlink) is never touched.
func DerivePRConfig(live []byte, beadsDir string) ([]byte, error) {
	if beadsDir == "" {
		return nil, fmt.Errorf("pg-pr config: a beads directory to pin --beads-dir to is required")
	}
	var doc any
	if err := yaml.Unmarshal(live, &doc); err != nil {
		return nil, fmt.Errorf("pg-pr config: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("pg-pr config is empty")
	}
	out, err := pinBeadsDir(stripJira(doc), beadsDir)
	if err != nil {
		return nil, fmt.Errorf("pg-pr config: %w", err)
	}
	return yaml.Marshal(out)
}

func isJira(s string) bool { return strings.Contains(strings.ToLower(s), "jira") }

// isJiraEntry reports whether a list element registers the Jira backend: a
// string naming it, or a {name, command} instance whose name or binary does.
func isJiraEntry(e any) bool {
	switch t := e.(type) {
	case string:
		return isJira(t)
	case map[string]any:
		if n, ok := t["name"].(string); ok && isJira(n) {
			return true
		}
		if cmd, ok := t["command"].([]any); ok && len(cmd) > 0 {
			if bin, ok := cmd[0].(string); ok && isJira(bin) {
				return true
			}
		}
	}
	return false
}

func stripJira(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range t {
			if isJira(k) {
				continue
			}
			out[k] = stripJira(val)
		}
		return out
	case []any:
		var out []any
		for _, e := range t {
			if isJiraEntry(e) {
				continue
			}
			out = append(out, stripJira(e))
		}
		if out == nil {
			out = []any{}
		}
		return out
	}
	return v
}

// pinBeadsDir rewrites the directory of every --beads-dir word in every list.
func pinBeadsDir(v any, dir string) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			nv, err := pinBeadsDir(val, dir)
			if err != nil {
				return nil, err
			}
			t[k] = nv
		}
	case []any:
		for i := range t {
			if _, valueIdx, ok := safety.BeadsDirWord(t, i); ok {
				if valueIdx < 0 {
					return nil, fmt.Errorf("a %s word has no directory to pin", safety.BeadsDirFlag)
				}
				if valueIdx == i {
					t[i] = safety.BeadsDirFlag + "=" + dir
				} else {
					t[valueIdx] = dir
				}
			}
			nv, err := pinBeadsDir(t[i], dir)
			if err != nil {
				return nil, err
			}
			t[i] = nv
		}
	}
	return v, nil
}

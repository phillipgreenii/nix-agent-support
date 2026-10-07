package scratch

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// DeskParams are the values the scratch pg-desk config overrides.
type DeskParams struct {
	Queries      []string
	SweepMaxAge  string
	HermeticBD   bool
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
		delete(repo, "beads_dir")
		info.BeadsDir = ""
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

// DerivePRConfig removes the Jira backend from a live pg-pr (pg-connector)
// config: Jira entries in every string list and every backend map key that
// names it. The result is a standalone file, so the live config (a read-only
// nix-store symlink) is never touched.
func DerivePRConfig(live []byte) ([]byte, error) {
	var doc any
	if err := yaml.Unmarshal(live, &doc); err != nil {
		return nil, fmt.Errorf("pg-pr config: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("pg-pr config is empty")
	}
	return yaml.Marshal(stripJira(doc))
}

func isJira(s string) bool { return strings.Contains(strings.ToLower(s), "jira") }

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
			if s, ok := e.(string); ok && isJira(s) {
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

// Package configinventory is the evidence provider for the config-health
// lens. It reads memory, skills, instructions and settings from disk,
// resolves symlinks, keeps only the layers that load in the scope, and
// computes the mechanical facts (missing reference targets, duplicated text,
// settings keys defined twice with different values, oversize files, skills
// with no calls in the window) as evidence rows. Every row's defined_in is
// the layer of the resolved real path, and no row carries an absolute path.
package configinventory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/provider/ccaudit"
)

// QueryName is the canned query name the provider answers to; it is written
// into every row and listed for the config-health lens.
const QueryName = "inventory"

// ErrUsage marks a caller mistake (bad flag value); the command exits 2.
var ErrUsage = ccaudit.ErrUsage

// Thresholds, each a deterministic script check (see the notes file).
const (
	MaxInstructionLines = 200
	MaxSkillLines       = 500
	MaxBytes            = 25000
	MinDuplicateBytes   = 40
	MinUnusedWindow     = 7 * 24 * time.Hour
	maxSamples          = 5
)

// Options is one provider invocation.
type Options struct {
	Since, Until       string // RFC3339 with an offset (or Z); normalised to UTC Z
	Scope              string
	ScopePrefixes      []string // the first is the scope root
	Layers             []string
	ExcludeCwdPrefixes []string
	UserDir            string       // empty: $CLAUDE_CONFIG_DIR, else $HOME/.claude
	Home               string       // empty: $HOME; expands "~/" in references
	Exec               ccaudit.Exec // nil runs pg-ccaudit from PATH
}

func execPgCcaudit(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "pg-ccaudit", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pg-ccaudit %v: %w: %s", args, err, bytes.TrimSpace(errb.Bytes()))
	}
	return out.Bytes(), nil
}

type draft struct {
	kind, subject, typ, definedIn string
	metrics                       map[string]float64
	samples                       []string
	times                         []time.Time
}

func normText(b []byte) string { return strings.Join(strings.Fields(string(b)), " ") }

func short(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:n]
}

func lineCount(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte("\n"))
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// Rows computes the evidence rows.
func Rows(ctx context.Context, o Options) ([]ccaudit.Row, error) {
	from, err := ccaudit.NormalizeBound(o.Since)
	if err != nil {
		return nil, err
	}
	to, err := ccaudit.NormalizeBound(o.Until)
	if err != nil {
		return nil, err
	}
	fromT, _ := time.Parse(time.RFC3339Nano, from)
	toT, _ := time.Parse(time.RFC3339Nano, to)
	if toT.Before(fromT) {
		return nil, fmt.Errorf("%w: --until is before --since", ErrUsage)
	}
	if o.Scope == "" {
		return nil, fmt.Errorf("%w: --scope is required", ErrUsage)
	}
	for _, l := range o.Layers {
		if l != LayerUser && l != LayerRules && l != LayerProject {
			return nil, fmt.Errorf("%w: unknown layer %q (want user, rules or project)", ErrUsage, l)
		}
		if l == LayerProject && len(o.ScopePrefixes) == 0 {
			return nil, fmt.Errorf("%w: layer project needs a --scope-prefix", ErrUsage)
		}
	}
	home := o.Home
	if home == "" {
		home = os.Getenv("HOME")
	}
	userDir := o.UserDir
	if userDir == "" {
		if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
			userDir = d
		} else if home != "" {
			userDir = filepath.Join(home, ".claude")
		}
	}
	scopeRootRaw := ""
	if len(o.ScopePrefixes) > 0 {
		scopeRootRaw = o.ScopePrefixes[0]
	}
	c := newCollector(scopeRootRaw, userDir)
	if err := c.collect(o.Layers, scopeRootRaw); err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	var ds []draft
	ds = append(ds, staleRefs(c, home)...)
	ds = append(ds, oversize(c)...)
	ds = append(ds, duplicates(c)...)
	conf, err := conflicts(c)
	if err != nil {
		return nil, err
	}
	ds = append(ds, conf...)
	if toT.Sub(fromT) >= MinUnusedWindow {
		un, err := unused(ctx, c, o, from, to, fromT)
		if err != nil {
			return nil, err
		}
		ds = append(ds, un...)
	}
	return finish(ds, o.Scope, from, to), nil
}

func finish(ds []draft, scope, from, to string) []ccaudit.Row {
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].kind != ds[j].kind {
			return ds[i].kind < ds[j].kind
		}
		return ds[i].subject < ds[j].subject
	})
	rows := make([]ccaudit.Row, 0, len(ds))
	for i, d := range ds {
		first, last := from, to
		if len(d.times) > 0 {
			sort.Slice(d.times, func(a, b int) bool { return d.times[a].Before(d.times[b]) })
			first = d.times[0].Format(time.RFC3339)
			last = d.times[len(d.times)-1].Format(time.RFC3339)
		}
		sort.Strings(d.samples)
		if len(d.samples) > maxSamples {
			d.samples = d.samples[:maxSamples]
		}
		if d.samples == nil {
			d.samples = []string{}
		}
		rows = append(rows, ccaudit.Row{
			SchemaVersion: 1, ID: fmt.Sprintf("E-%03d", i+1), SubjectKey: d.subject,
			DefinedIn: d.definedIn, Type: d.typ, Kind: d.kind, Scope: scope,
			Window:  ccaudit.Window{From: from, To: to},
			Metrics: d.metrics, Baseline: []any{}, FirstSeen: first, LastSeen: last,
			Query: QueryName, Samples: d.samples,
		})
	}
	return rows
}

var (
	fenceRE  = regexp.MustCompile("(?s)```.*?```")
	mdLinkRE = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	importRE = regexp.MustCompile(`(?m)(?:^|\s)@((?:~|\.{1,2})?/[^\s)]+)`)
)

func isLocalTarget(t string) bool {
	return t != "" && !strings.Contains(t, "://") && !strings.HasPrefix(t, "mailto:") && !strings.HasPrefix(t, "#")
}

func refTargets(data []byte) []string {
	text := fenceRE.ReplaceAllString(string(data), "")
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		if i := strings.IndexAny(t, "#?"); i >= 0 {
			t = t[:i]
		}
		t = strings.TrimRight(t, ".,;:")
		if isLocalTarget(t) && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, m := range mdLinkRE.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range importRE.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	sort.Strings(out)
	return out
}

// shownTarget renders a reference target without any absolute path: a
// relative or "~/" target stays as written, an absolute one is shown relative
// to the scope root or reduced to its base name.
func shownTarget(c *collector, target string) string {
	if !strings.HasPrefix(target, "/") {
		return target
	}
	if c.scopeRoot != "" && strings.HasPrefix(target, c.scopeRoot+"/") {
		return strings.TrimPrefix(target, c.scopeRoot+"/")
	}
	return "<abs>/" + filepath.Base(target)
}

func staleRefs(c *collector, home string) []draft {
	var ds []draft
	for _, e := range c.entries {
		if e.kind == kindSettings {
			continue
		}
		for _, t := range refTargets(e.data) {
			var p string
			switch {
			case strings.HasPrefix(t, "~/"):
				if home == "" {
					continue
				}
				p = filepath.Join(home, t[2:])
			case strings.HasPrefix(t, "/"):
				p = t
			default:
				p = filepath.Join(filepath.Dir(e.real), t)
			}
			if _, err := os.Stat(p); err == nil || !os.IsNotExist(err) {
				continue
			}
			shown := shownTarget(c, t)
			ds = append(ds, draft{
				kind: "stale-reference", typ: "reference", definedIn: e.definedIn,
				subject: fmt.Sprintf("ref:%s:%s->%s", e.definedIn, e.rel, shown),
				metrics: map[string]float64{"missing": 1},
				samples: []string{fmt.Sprintf("%s -> %s", describe(e), shown)},
				times:   []time.Time{e.mtime},
			})
		}
	}
	for _, d := range c.dangling {
		ds = append(ds, draft{
			kind: "stale-reference", typ: "reference", definedIn: d.definedIn,
			subject: fmt.Sprintf("link:%s:%s", d.definedIn, d.rel),
			metrics: map[string]float64{"dangling_symlink": 1},
			samples: []string{fmt.Sprintf("%s:%s is a symlink whose target is missing", d.definedIn, d.rel)},
		})
	}
	return ds
}

func oversize(c *collector) []draft {
	var ds []draft
	for _, e := range c.entries {
		if e.kind == kindSettings {
			continue
		}
		lines, limit := lineCount(e.data), MaxInstructionLines
		if e.kind == kindSkill {
			limit = MaxSkillLines
		}
		if lines <= limit && len(e.data) <= MaxBytes {
			continue
		}
		subject := fmt.Sprintf("%s:%s:%s", e.kind, e.definedIn, e.rel)
		if e.kind == kindSkill {
			subject = fmt.Sprintf("skill:%s:%s", e.definedIn, e.names[0])
		}
		ds = append(ds, draft{
			kind: "oversize", typ: e.kind, definedIn: e.definedIn, subject: subject,
			metrics: map[string]float64{
				"lines": float64(lines), "bytes": float64(len(e.data)),
				"limit_lines": float64(limit), "limit_bytes": MaxBytes,
			},
			samples: []string{describe(e)}, times: []time.Time{e.mtime},
		})
	}
	return ds
}

func textEntries(c *collector) []*entry {
	var out []*entry
	for _, e := range c.entries {
		if e.kind != kindSettings {
			out = append(out, e)
		}
	}
	return out
}

func duplicates(c *collector) []draft {
	groups := map[string][]*entry{}
	for _, e := range textEntries(c) {
		n := normText(e.data)
		if len(n) < MinDuplicateBytes {
			continue
		}
		h := short(n, 12)
		groups[h] = append(groups[h], e)
	}
	var ds []draft
	for h, es := range groups {
		if len(es) < 2 {
			continue
		}
		sort.Slice(es, func(i, j int) bool { return describe(es[i]) < describe(es[j]) })
		d := draft{
			kind: "duplicate", typ: "text", definedIn: es[0].definedIn, subject: "dup:" + h,
			metrics: map[string]float64{"copies": float64(len(es)), "bytes": float64(len(normText(es[0].data)))},
		}
		for _, e := range es {
			d.samples = append(d.samples, describe(e))
			d.times = append(d.times, e.mtime)
		}
		ds = append(ds, d)
	}
	return ds
}

func conflicts(c *collector) ([]draft, error) {
	type def struct {
		e     *entry
		value string
	}
	byKey := map[string][]def{}
	for _, e := range c.entries {
		if e.kind != kindSettings {
			continue
		}
		leaves, err := settingLeaves(e.data)
		if err != nil {
			return nil, fmt.Errorf("read configuration: %s is not valid JSON: %w", describe(e), err)
		}
		for k, v := range leaves {
			byKey[k] = append(byKey[k], def{e, v})
		}
	}
	var ds []draft
	for k, defs := range byKey {
		vals := map[string]bool{}
		for _, d := range defs {
			vals[d.value] = true
		}
		if len(vals) < 2 {
			continue
		}
		sort.Slice(defs, func(i, j int) bool { return describe(defs[i].e) < describe(defs[j].e) })
		d := draft{
			kind: "conflict", typ: "setting", definedIn: defs[0].e.definedIn, subject: "setting:" + k,
			metrics: map[string]float64{"definitions": float64(len(defs)), "distinct_values": float64(len(vals))},
		}
		for _, x := range defs {
			d.samples = append(d.samples, fmt.Sprintf("%s value_sha=%s", describe(x.e), short(x.value, 8)))
			d.times = append(d.times, x.e.mtime)
		}
		ds = append(ds, d)
	}
	// One skill name defined by two different real files with different text.
	byName := map[string][]*entry{}
	for _, e := range c.entries {
		if e.kind == kindSkill {
			for _, n := range e.names {
				byName[n] = append(byName[n], e)
			}
		}
	}
	for n, es := range byName {
		texts := map[string]bool{}
		for _, e := range es {
			texts[normText(e.data)] = true
		}
		if len(es) < 2 || len(texts) < 2 {
			continue
		}
		sort.Slice(es, func(i, j int) bool { return describe(es[i]) < describe(es[j]) })
		d := draft{
			kind: "conflict", typ: "skill", definedIn: es[0].definedIn, subject: "skill-name:" + n,
			metrics: map[string]float64{"definitions": float64(len(es)), "distinct_values": float64(len(texts))},
		}
		for _, e := range es {
			d.samples = append(d.samples, describe(e))
			d.times = append(d.times, e.mtime)
		}
		ds = append(ds, d)
	}
	return ds, nil
}

type payload struct {
	Rows []map[string]any `json:"rows"`
}

func unused(ctx context.Context, c *collector, o Options, from, to string, fromT time.Time) ([]draft, error) {
	var skills []*entry
	for _, e := range c.entries {
		if e.kind == kindSkill && e.mtime.Before(fromT) {
			skills = append(skills, e)
		}
	}
	if len(skills) == 0 {
		return nil, nil
	}
	run := o.Exec
	if run == nil {
		run = execPgCcaudit
	}
	args := []string{"query", "skill-calls", "--since", from, "--until", to, "--format", "json", "--no-staleness"}
	for _, p := range o.ScopePrefixes {
		args = append(args, "--cwd-prefix", p)
	}
	for _, p := range o.ExcludeCwdPrefixes {
		args = append(args, "--exclude-cwd-prefix", p)
	}
	raw, err := run(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("query skill-calls: %w", err)
	}
	var p payload
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("query skill-calls: decode output: %w", err)
	}
	called := map[string]bool{}
	for _, r := range p.Rows {
		if s, ok := r["skill"].(string); ok && s != "" {
			called[s] = true
		}
	}
	used := func(name string) bool {
		if called[name] {
			return true
		}
		for k := range called {
			if strings.HasSuffix(k, ":"+name) {
				return true
			}
		}
		return false
	}
	var ds []draft
	for _, e := range skills {
		hit := false
		for _, n := range e.names {
			hit = hit || used(n)
		}
		if hit {
			continue
		}
		ds = append(ds, draft{
			kind: "unused", typ: kindSkill, definedIn: e.definedIn,
			subject: fmt.Sprintf("skill:%s:%s", e.definedIn, e.names[0]),
			metrics: map[string]float64{
				"calls": 0, "age_days": float64(int(fromT.Sub(e.mtime) / (24 * time.Hour))),
			},
			samples: []string{describe(e)}, times: []time.Time{e.mtime},
		})
	}
	return ds, nil
}

// Encode writes the rows as one indented JSON array.
func Encode(w interface{ Write([]byte) (int, error) }, rows []ccaudit.Row) error {
	return ccaudit.Encode(w, rows)
}

// Run is Rows followed by Encode; nothing is written on failure.
func Run(ctx context.Context, o Options, w interface{ Write([]byte) (int, error) }) error {
	rows, err := Rows(ctx, o)
	if err != nil {
		return err
	}
	return Encode(w, rows)
}

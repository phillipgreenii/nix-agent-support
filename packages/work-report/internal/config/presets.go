package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/rangespec"
)

// The generators and reports sections of the config. Load keeps both as raw
// YAML nodes and checks nothing inside them; Presets parses and checks them.
// It is called only by report, config show and config validate, so a mistake
// in either section never affects pull, query or status, and the built-in
// baseline generator and preset are returned whatever else is wrong.

const (
	// BaselineName names both the built-in generator and the built-in preset.
	BaselineName = "baseline"
	// DefaultGeneratorTimeout applies to a generator that sets no timeout.
	DefaultGeneratorTimeout = 5 * time.Minute
	// DefaultPresetRange applies to a preset that sets no range.
	DefaultPresetRange = "today"
	// Redacted replaces a config value whose key looks secret.
	Redacted = "***"
)

// The problem kinds Presets emits. config validate reports them under these
// names; a problem in no listed class is bad_field.
const (
	ProblemBadName          = "bad_name"
	ProblemBadCommand       = "bad_command"
	ProblemReservedName     = "reserved_name"
	ProblemUnknownGenerator = "unknown_generator"
	ProblemBadRange         = "bad_range"
	ProblemBadInputs        = "bad_inputs"
	ProblemBadOutputDir     = "bad_output_dir"
	ProblemBadField         = "bad_field"
)

// Generator is a registered report generator.
type Generator struct {
	Name string
	// Builtin is true for the built-in baseline generator, which has no
	// command.
	Builtin bool
	// Command is the argv; a bare first element is resolved on PATH when a
	// run starts.
	Command []string
	// Timeout is the run limit; DefaultGeneratorTimeout when unset. Zero for
	// the built-in generator.
	Timeout time.Duration
	// Inputs holds the extra inputs the generator asks for; the only legal
	// value is "baseline".
	Inputs []string
	// Env lists the environment variable NAMES passed through to the run.
	Env []string
}

// Narrowing is a preset's filter. Values within a dimension are OR, across
// dimensions AND.
type Narrowing struct {
	Labels, Sources, Types []string
}

// Preset is a named report definition.
type Preset struct {
	Name string
	// Builtin is true for the built-in baseline preset.
	Builtin   bool
	Generator string
	// Config is the free-form generator config, JSON-object compatible.
	Config map[string]any
	// Range is a range spec string, DefaultPresetRange when unset.
	Range     string
	Narrowing Narrowing
	// OutputDir is the absolute PARENT directory of the per-run directories;
	// the default parent when the preset sets none.
	OutputDir string
}

// Problem is one thing wrong in the generators or reports section.
type Problem struct {
	// Kind is one of the Problem* constants.
	Kind string
	// Generator or Preset names the entry the problem belongs to; both are
	// empty for a problem in neither (for example a section that is not a
	// mapping).
	Generator, Preset string
	Message           string
}

// PresetSet is Presets' result.
type PresetSet struct {
	// Generators and Presets hold the built-in baseline and every entry that
	// has no problem.
	Generators map[string]Generator
	Presets    map[string]Preset
	// Problems is ordered by section (generators first), then entry name.
	Problems []Problem
}

var (
	nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	envRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// sensitiveKeyRE is the unanchored rule of the manifest redaction: a
	// config key containing any of these words, in any case.
	sensitiveKeyRE = regexp.MustCompile(`(?i)key|token|secret|password|credential`)
)

// Presets parses and checks the generators and reports sections of c. The
// result always holds the built-in baseline generator and preset and every
// valid entry, however many problems the rest has.
func Presets(c Config) PresetSet {
	out := PresetSet{
		Generators: map[string]Generator{BaselineName: {Name: BaselineName, Builtin: true, Inputs: []string{}, Env: []string{}}},
		Presets: map[string]Preset{BaselineName: {
			Name: BaselineName, Builtin: true, Generator: BaselineName, Config: map[string]any{},
			Range: DefaultPresetRange, Narrowing: emptyNarrowing(), OutputDir: defaultOutputDir(BaselineName),
		}},
	}
	out.parseGenerators(&c.generators)
	loc := time.UTC
	if l, err := c.Location(); err == nil {
		loc = l
	}
	out.parseReports(&c.reports, loc)
	return out
}

func emptyNarrowing() Narrowing {
	return Narrowing{Labels: []string{}, Sources: []string{}, Types: []string{}}
}

func defaultOutputDir(preset string) string {
	return filepath.Join(stateHome(), "work-report", "reports", preset)
}

// entry is one key of a section or entry mapping.
type entry struct {
	name    string
	nameOK  bool // the key is a plain string scalar
	keyNode *yaml.Node
	val     *yaml.Node
}

// entries lists the key/value pairs of a mapping node, sorted by key. An
// absent or null node is an empty mapping; any other non-mapping is an error.
func entries(n *yaml.Node) ([]entry, error) {
	n = resolve(n)
	if n == nil || n.Kind == 0 || (n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null") {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("want a mapping, got %s", kindName(n))
	}
	var es []entry
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := resolve(n.Content[i])
		name, ok := scalarString(k)
		es = append(es, entry{name: name, nameOK: ok, keyNode: k, val: n.Content[i+1]})
	}
	sort.SliceStable(es, func(i, j int) bool { return es[i].name < es[j].name })
	return es, nil
}

func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

func kindName(n *yaml.Node) string {
	switch resolve(n).Kind {
	case yaml.SequenceNode:
		return "a list"
	case yaml.ScalarNode:
		return "a scalar"
	case yaml.MappingNode:
		return "a mapping"
	}
	return "something else"
}

// scalarString returns the value of a string-tagged scalar node. A number or
// bool (an unquoted 123 or true) is not a string here: it must be quoted.
func scalarString(n *yaml.Node) (string, bool) {
	n = resolve(n)
	if n == nil || n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" {
		return "", false
	}
	return n.Value, true
}

// stringList returns the elements of a list of strings. An absent or null node
// is the empty list.
func stringList(n *yaml.Node) ([]string, bool) {
	n = resolve(n)
	if n == nil || n.Kind == 0 || (n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null") {
		return []string{}, true
	}
	if n.Kind != yaml.SequenceNode {
		return nil, false
	}
	out := make([]string, 0, len(n.Content))
	for _, e := range n.Content {
		s, ok := scalarString(e)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// checkName reports the name problem of a section key, if any.
func checkName(e entry, isGenerator bool) (Problem, bool) {
	p := Problem{}
	if isGenerator {
		p.Generator = e.name
	} else {
		p.Preset = e.name
	}
	switch {
	case !e.nameOK:
		p.Generator, p.Preset = "", ""
		p.Kind, p.Message = ProblemBadName, "an entry name must be a string"
	case e.name == BaselineName:
		p.Kind, p.Message = ProblemReservedName, "baseline is built in and cannot be redefined"
	case !nameRE.MatchString(e.name):
		p.Kind, p.Message = ProblemBadName, fmt.Sprintf("name %q must match [a-z0-9][a-z0-9_-]*", e.name)
	default:
		return Problem{}, false
	}
	return p, true
}

func (s *PresetSet) parseGenerators(n *yaml.Node) {
	es, err := entries(n)
	if err != nil {
		s.Problems = append(s.Problems, Problem{Kind: ProblemBadField, Message: "generators: " + err.Error()})
		return
	}
	for _, e := range es {
		if p, bad := checkName(e, true); bad {
			s.Problems = append(s.Problems, p)
			continue
		}
		g, probs := parseGenerator(e.name, e.val)
		if len(probs) > 0 {
			s.Problems = append(s.Problems, probs...)
			continue
		}
		s.Generators[e.name] = g
	}
}

func parseGenerator(name string, n *yaml.Node) (Generator, []Problem) {
	g := Generator{Name: name, Timeout: DefaultGeneratorTimeout, Inputs: []string{}, Env: []string{}}
	var probs []Problem
	add := func(kind, format string, args ...any) {
		probs = append(probs, Problem{Kind: kind, Generator: name, Message: fmt.Sprintf(format, args...)})
	}
	fields, err := entries(n)
	if err != nil {
		add(ProblemBadField, "generator %s: %v", name, err)
		return g, probs
	}
	haveCommand := false
	for _, f := range fields {
		switch f.name {
		case "command":
			haveCommand = true
			argv, ok := stringList(f.val)
			switch {
			case !ok:
				add(ProblemBadCommand, "generators.%s.command must be a list of strings (quote any number or boolean)", name)
			case len(argv) == 0 || argv[0] == "":
				add(ProblemBadCommand, "generators.%s.command must be a non-empty argv list with a non-empty first element", name)
			default:
				g.Command = argv
			}
		case "timeout":
			s, ok := scalarString(f.val)
			d, perr := time.ParseDuration(s)
			if !ok || perr != nil || d <= 0 {
				add(ProblemBadField, "generators.%s.timeout must be a positive duration string such as 10m", name)
				continue
			}
			g.Timeout = d
		case "inputs":
			in, ok := stringList(f.val)
			if !ok {
				add(ProblemBadInputs, "generators.%s.inputs must be a list of strings", name)
				continue
			}
			for _, v := range in {
				if v != BaselineName {
					add(ProblemBadInputs, "generators.%s.inputs: %q is not allowed; the only input is baseline", name, v)
				}
			}
			g.Inputs = in
		case "env":
			names, ok := stringList(f.val)
			if !ok {
				add(ProblemBadField, "generators.%s.env must be a list of environment variable names", name)
				continue
			}
			for _, v := range names {
				if !envRE.MatchString(v) {
					add(ProblemBadField, "generators.%s.env: %q is not an environment variable name", name, v)
				}
			}
			g.Env = names
		default:
			add(ProblemBadField, "generators.%s: unknown key %q", name, f.name)
		}
	}
	if !haveCommand {
		add(ProblemBadCommand, "generators.%s.command is required", name)
	}
	return g, probs
}

func (s *PresetSet) parseReports(n *yaml.Node, loc *time.Location) {
	es, err := entries(n)
	if err != nil {
		s.Problems = append(s.Problems, Problem{Kind: ProblemBadField, Message: "reports: " + err.Error()})
		return
	}
	for _, e := range es {
		if p, bad := checkName(e, false); bad {
			s.Problems = append(s.Problems, p)
			continue
		}
		p, probs := s.parsePreset(e.name, e.val, loc)
		if len(probs) > 0 {
			s.Problems = append(s.Problems, probs...)
			continue
		}
		s.Presets[e.name] = p
	}
}

func (s *PresetSet) parsePreset(name string, n *yaml.Node, loc *time.Location) (Preset, []Problem) {
	p := Preset{Name: name, Config: map[string]any{}, Range: DefaultPresetRange, Narrowing: emptyNarrowing(), OutputDir: defaultOutputDir(name)}
	var probs []Problem
	add := func(kind, format string, args ...any) {
		probs = append(probs, Problem{Kind: kind, Preset: name, Message: fmt.Sprintf(format, args...)})
	}
	fields, err := entries(n)
	if err != nil {
		add(ProblemBadField, "reports.%s: %v", name, err)
		return p, probs
	}
	haveGenerator, haveConfig := false, false
	for _, f := range fields {
		switch f.name {
		case "generator":
			haveGenerator = true
			g, ok := scalarString(f.val)
			if !ok || g == "" {
				add(ProblemBadField, "reports.%s.generator must be a generator name", name)
				continue
			}
			if _, reg := s.Generators[g]; !reg {
				add(ProblemUnknownGenerator, "reports.%s.generator names %q, which is not a registered generator", name, g)
				continue
			}
			p.Generator = g
		case "range":
			spec, ok := scalarString(f.val)
			if _, rerr := rangespec.Resolve(spec, time.Now(), loc); !ok || spec == "" || rerr != nil {
				add(ProblemBadRange, "reports.%s.range %q is not a range spec (%s)", name, spec, rangespec.Forms)
				continue
			}
			p.Range = spec
		case "narrowing":
			p.Narrowing = parseNarrowing(name, f.val, add)
		case "config":
			haveConfig = true
			m, cerr := configMap(f.val)
			if cerr != nil {
				add(ProblemBadField, "reports.%s.config: %v", name, cerr)
				continue
			}
			p.Config = m
		case "output":
			dir, ok := parseOutput(name, f.val, add)
			if ok {
				p.OutputDir = dir
			}
		default:
			add(ProblemBadField, "reports.%s: unknown key %q", name, f.name)
		}
	}
	if !haveGenerator {
		add(ProblemBadField, "reports.%s.generator is required", name)
	}
	if haveConfig && p.Generator == BaselineName {
		add(ProblemBadField, "reports.%s.config is not allowed: the baseline generator takes no config", name)
	}
	return p, probs
}

func parseNarrowing(name string, n *yaml.Node, add func(kind, format string, args ...any)) Narrowing {
	out := emptyNarrowing()
	fields, err := entries(n)
	if err != nil {
		add(ProblemBadField, "reports.%s.narrowing: %v", name, err)
		return out
	}
	for _, f := range fields {
		vals, ok := stringList(f.val)
		if !ok {
			add(ProblemBadField, "reports.%s.narrowing.%s must be a list of strings", name, f.name)
			continue
		}
		switch f.name {
		case "labels":
			out.Labels = vals
		case "sources":
			out.Sources = vals
		case "types":
			out.Types = vals
		default:
			add(ProblemBadField, "reports.%s.narrowing: unknown key %q", name, f.name)
		}
	}
	return out
}

func parseOutput(name string, n *yaml.Node, add func(kind, format string, args ...any)) (string, bool) {
	fields, err := entries(n)
	if err != nil {
		add(ProblemBadField, "reports.%s.output: %v", name, err)
		return "", false
	}
	dir, ok := "", false
	for _, f := range fields {
		if f.name != "dir" {
			add(ProblemBadField, "reports.%s.output: unknown key %q", name, f.name)
			continue
		}
		raw, isStr := scalarString(f.val)
		if !isStr {
			add(ProblemBadOutputDir, "reports.%s.output.dir must be a string", name)
			continue
		}
		d, derr := ExpandOutputDir(raw)
		if derr != nil {
			add(ProblemBadOutputDir, "reports.%s.output.dir %q: %v", name, raw, derr)
			continue
		}
		dir, ok = d, true
	}
	return dir, ok
}

// ExpandOutputDir resolves an output directory setting to an absolute path.
// A leading bare ~ expands to $HOME (~user is rejected) and $VAR / ${VAR}
// expand from the environment, with $XDG_STATE_HOME falling back as the rest
// of the loader does. A variable that is unset or empty is an error, never the
// empty string, so a typo cannot silently turn the directory into a different
// absolute path. The result is cleaned.
func ExpandOutputDir(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("is empty")
	}
	prefix := ""
	if strings.HasPrefix(s, "~") {
		if s != "~" && !strings.HasPrefix(s, "~/") {
			return "", fmt.Errorf("~user is not expanded; use a bare ~ or an absolute path")
		}
		home := os.Getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("~ cannot be expanded: HOME is unset")
		}
		prefix, s = home, s[1:]
	}
	var missing []string
	expanded := os.Expand(s, func(name string) string {
		if name == "XDG_STATE_HOME" && (os.Getenv(name) != "" || os.Getenv("HOME") != "") {
			return stateHome()
		}
		v := os.Getenv(name)
		if v == "" {
			missing = append(missing, "$"+name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("references unset or empty %s", strings.Join(missing, ", "))
	}
	out := prefix + expanded
	if !filepath.IsAbs(out) {
		return "", fmt.Errorf("must resolve to an absolute path, got %q", out)
	}
	return filepath.Clean(out), nil
}

// configMap decodes a preset's config node to a JSON-object-compatible map.
func configMap(n *yaml.Node) (map[string]any, error) {
	n = resolve(n)
	if n == nil || n.Kind == 0 || (n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null") {
		return map[string]any{}, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("want a mapping, got %s", kindName(n))
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, err
	}
	norm, err := normalize(v)
	if err != nil {
		return nil, err
	}
	m, _ := norm.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// normalize rewrites decoded YAML into JSON-compatible values: every map is a
// map[string]any, every list a []any.
func normalize(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			ne, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[k] = ne
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("key %v is not a string", k)
			}
			ne, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[ks] = ne
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			ne, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[i] = ne
		}
		return out, nil
	}
	return v, nil
}

// Redact returns a deep copy of a generator config with every value under a
// key matching the unanchored, case-insensitive pattern
// key|token|secret|password|credential replaced by Redacted, at any depth. An
// object or array value under a matching key is replaced as a whole. The
// over-redaction (monkey, maxTokens, sortKey) is intended: it is cheap, and a
// config hash still identifies the config. This is the one redaction rule:
// config show and the run manifest both use it.
func Redact(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sensitiveKeyRE.MatchString(k) {
			out[k] = Redacted
			continue
		}
		out[k] = redactValue(v)
	}
	return out
}

func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return Redact(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactValue(e)
		}
		return out
	}
	return v
}

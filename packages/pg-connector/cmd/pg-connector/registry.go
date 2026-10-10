// registry.go: the connector.<type> backend registry loader.
//
// Config format, resolution order ($PG_PR_CONFIG -> $XDG_CONFIG_HOME ->
// ~/.config), and YAML shape carry over unchanged from pg-pr's existing
// config machinery in packages/pg-pr/internal/config — including the
// literal env-var name $PG_PR_CONFIG, which is NOT renamed even though the
// binary is now pg-connector. pg-connector and pg-pr can share a single
// config.yaml on a host running both: pg-connector only looks at the
// connector: key; everything else in the file is simply ignored (YAML
// decoding here is unknown-fields-tolerant, matching pg-pr's own decode).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNoConfig is returned when no config file is found via the resolution
// order and $PG_PR_CONFIG was not set.
var ErrNoConfig = errors.New("registry: no config file found")

// Registry is the parsed connector.<type> registry: for each entity-type
// key, either a list of backend registrations (issue/ci/pr today) or a
// single one (scm today). A registration is a bare binary name on PATH or an
// instance {name, command} (INV-REG-4, registry_backend.go): command is an
// argv list whose first word is a bare binary name, so one binary can be
// registered more than once under different names. There is no exec:-prefix
// distinction anywhere in this registry, since nothing is compiled in.
// Accessors return NAMES (the instance identity); Command/Target resolve a
// name to the argv the umbrella execs.
//
// attentionSources/searchSources/activitySources hold the top-level
// attention.sources / search.sources / activity.sources registrations —
// always list-valued, and independent of connector.<type> (never read from
// raw). nil means the key (or its nested sources: sub-key) was absent; a
// non-nil slice — including a non-nil empty one from an explicit
// sources: [] — came from an actual sources: entry. That absent-vs-empty
// distinction is exactly what the source accessors need and is preserved
// unchanged from how parseRegistry decodes it.
type Registry struct {
	raw              map[string]yaml.Node
	attentionSources []yaml.Node
	searchSources    []yaml.Node
	activitySources  []yaml.Node
	backends         map[string]yaml.Node
	state            map[string]yaml.Node
	// commands maps a registered name to its argv, filled at parse time by
	// a best-effort pass over every registration that decodes cleanly.
	commands map[string][]string
	// previousNames maps an instance name to its declared previous_names
	// (bead pg2-ik9ew); only names that declare some appear.
	previousNames map[string][]string
}

type registryDoc struct {
	Connector map[string]yaml.Node `yaml:"connector"`
	Attention *sourcesDoc          `yaml:"attention"`
	Search    *sourcesDoc          `yaml:"search"`
	Activity  *sourcesDoc          `yaml:"activity"`
	// Backends is the top-level backends.<binary> map (bead pg2-2j5ac.28.1,
	// bead pg2-2j5ac.28.1) — each entry's own opaque config block, copied VERBATIM
	// by Invoke into every wire request sent to that binary. It is a
	// SIBLING of connector: (not nested under it): a backend name may be
	// registered under more than one connector.<type> entry (a
	// multi-capability backend, INV-REG-1), and this config is per-BINARY,
	// not per-(type, binary) pair.
	Backends map[string]yaml.Node `yaml:"backends"`
	// State is the top-level state: map (bead pg2-2j5ac.30.1) — the
	// already-landed home/programs/pg-connector state option's arbitrary
	// attrset, rendered verbatim into the shared config file (e.g.
	// state.consumer_prune_after). This packet is the first Go-side reader
	// of it, via Registry.StateValue below; nothing else in this module
	// parses it.
	State map[string]yaml.Node `yaml:"state"`
}

// sourcesDoc is the shape of the top-level attention:/search:/activity: mappings: a
// mapping with a single nested sources: list key, siblings of connector:
// rather than members of it.
type sourcesDoc struct {
	Sources []yaml.Node `yaml:"sources"`
}

// envSource is the minimal interface LoadRegistry needs to look up env +
// home dir. Exposed so tests can inject a fixed environment.
type envSource interface {
	Getenv(string) string
	UserHomeDir() (string, error)
}

type osEnv struct{}

func (osEnv) Getenv(k string) string       { return os.Getenv(k) }
func (osEnv) UserHomeDir() (string, error) { return os.UserHomeDir() }

// LoadRegistry loads the connector.<type> registry using pg-pr's existing
// config resolution order and YAML shape.
func LoadRegistry() (*Registry, error) {
	return loadRegistryFromEnv(osEnv{})
}

// ResolveConfigPath resolves which config file LoadRegistry would read,
// via the same $PG_PR_CONFIG -> XDG -> ~/.config resolution order, without
// parsing it — used by "config show" (config_show.go) to report the
// resolved path even before/regardless of whether its contents parse.
func ResolveConfigPath() (string, error) {
	return resolveConfigPath(osEnv{})
}

func loadRegistryFromEnv(env envSource) (*Registry, error) {
	path, err := resolveConfigPath(env)
	if err != nil {
		return nil, err
	}
	return loadRegistryFile(path)
}

// resolveConfigPath is loadRegistryFromEnv's own path-resolution step,
// factored out so "config show" can report which config file was resolved
// without also parsing it — the exact same $PG_PR_CONFIG -> XDG ->
// ~/.config resolution order lives in exactly this one place.
func resolveConfigPath(env envSource) (string, error) {
	if explicit := env.Getenv("PG_PR_CONFIG"); explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("registry: $PG_PR_CONFIG=%s does not exist", explicit)
			}
			return "", err
		}
		return explicit, nil
	}

	candidates := registryCandidates(env)
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("%w: looked in %s; create one or set $PG_PR_CONFIG",
		ErrNoConfig, strings.Join(candidates, ", "))
}

// registryCandidates returns the list of paths LoadRegistry checks, in
// order, when $PG_PR_CONFIG is unset. The directory name stays "pg-pr"
// (not "pg-connector"): this is deliberately the SAME config file pg-pr
// itself reads.
func registryCandidates(env envSource) []string {
	var out []string
	if xdg := env.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		out = append(out, filepath.Join(xdg, "pg-pr", "config.yaml"))
	}
	if home, err := env.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, ".config", "pg-pr", "config.yaml"))
	}
	return out
}

func loadRegistryFile(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("registry: read %s: %w", path, err)
	}
	return parseRegistry(data, path)
}

func parseRegistry(data []byte, path string) (*Registry, error) {
	var doc registryDoc
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	// KnownFields stays false at THIS (top) level deliberately — see the
	// file header comment: pg-connector and pg-pr can share one
	// config.yaml, and everything outside the connector: key belongs to
	// pg-pr, not to us. It would be wrong to reject pg-pr's own keys here.
	dec.KnownFields(false)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	// Unlike the top level, keys UNDER connector: belong entirely to us —
	// there is no sibling tool whose keys could legitimately land there —
	// so an unrecognized one (a typo'd entity type, e.g. "prs" for "pr")
	// is rejected rather than silently ignored [bug A16]. Without this, a
	// typo'd key decodes to zero backends for that type and
	// AllBackends/config validate report exit 3 ("all backends down"),
	// indistinguishable from a genuinely all-down host.
	if err := validateConnectorKeys(doc.Connector); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	// A capability-only backend (pg-connector-activity-*) is registered only
	// under activity.sources; reject it under any connector.<type> at load
	// time so a bad config fails fast whichever accessor runs later.
	if err := validateNoCapabilityOnlyBackends(doc.Connector); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	reg := &Registry{raw: doc.Connector, backends: doc.Backends, state: doc.State}
	if doc.Attention != nil {
		reg.attentionSources = doc.Attention.Sources
	}
	if doc.Search != nil {
		reg.searchSources = doc.Search.Sources
	}
	if doc.Activity != nil {
		reg.activitySources = doc.Activity.Sources
	}
	// One best-effort pass over every registration that decodes cleanly: it
	// fills the name -> argv map and rejects a name registered with two
	// different commands. Entries that fail to decode are skipped here; the
	// accessors report them.
	regs := reg.registrations()
	commands, err := buildCommands(regs)
	if err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	reg.commands = commands
	if reg.previousNames, err = buildPreviousNames(regs, commands); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	return reg, nil
}

// validateConnectorKeys rejects any key under connector: that is not one
// of entityTypes.
func validateConnectorKeys(connector map[string]yaml.Node) error {
	known := make(map[string]bool, len(entityTypes))
	for _, t := range entityTypes {
		known[t] = true
	}
	var unknown []string
	for k := range connector {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("connector.%s: unknown key(s) under connector: — must be one of %s",
		strings.Join(unknown, ", "), strings.Join(entityTypes, ", "))
}

// capabilityOnlyBackendPrefix is the binary-name prefix reserved for
// capability-only backends: a backend that implements only list_activity,
// has no auth_status, and is therefore not a fan-out member of any
// connector.<type>. Such a binary belongs only under activity.sources.
const capabilityOnlyBackendPrefix = "pg-connector-activity-"

// validateNoCapabilityOnlyBackends rejects any registration whose name OR
// command[0] starts with capabilityOnlyBackendPrefix (including
// pg-connector-activity-git) found under a connector.<type> key, list-valued
// or single-valued alike. The error names the offending key and backend.
// Entries that do not decode are skipped here: List/Single report those
// shape errors themselves when an accessor runs.
func validateNoCapabilityOnlyBackends(connector map[string]yaml.Node) error {
	keys := make([]string, 0, len(connector))
	for k := range connector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		node := connector[k]
		for _, ref := range lenientRefs("connector."+k, connectorEntries(&node)) {
			if strings.HasPrefix(ref.Name, capabilityOnlyBackendPrefix) || strings.HasPrefix(ref.Command[0], capabilityOnlyBackendPrefix) {
				return fmt.Errorf("connector.%s: backend %q is a capability-only backend and must be registered only under activity.sources, not under connector.<type>", k, ref.Name)
			}
		}
	}
	return nil
}

// validateBackendName rejects a registered backend name that cannot be a
// bare binary name: empty, or containing a path separator. Every registry
// value is resolved as a bare binary name via PATH lookup (see this file's
// header comment) — an empty name is a config mistake, and a name
// containing a separator ("/" on every OS this repo targets, plus the
// platform's own filepath.Separator) is either that same mistake or an
// attempt to smuggle a path into what must stay a name.
//
// key names the specific registry key the name was registered under (e.g.
// "connector.pr", "attention.sources") — it is used verbatim in the error
// message, so callers pass the fully-qualified key rather than a bare
// entity type.
func validateBackendName(key, name string) error {
	if name == "" {
		return fmt.Errorf("registry: %s: backend name must not be empty", key)
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
		return fmt.Errorf("registry: %s: backend name %q must be a bare binary name, not a path", key, name)
	}
	return nil
}

func nodeKindName(k yaml.Kind) string {
	switch k {
	case yaml.SequenceNode:
		return "a list"
	case yaml.ScalarNode:
		return "a single value"
	case yaml.MappingNode:
		return "a mapping"
	case yaml.AliasNode:
		return "a YAML alias (aliases are not supported; write the value out)"
	default:
		return "an unrecognized YAML node"
	}
}

// List returns the names registered under connector.<entityType>, which
// must decode as a YAML list (pr/issue/ci today) of registered backend names:
// bare binary names and/or {name, command} instances (it returns the names,
// not the commands; see Command). An entityType with no entry returns (nil, nil).
func (r *Registry) List(entityType string) ([]string, error) {
	refs, err := r.listRefs(entityType)
	if err != nil {
		return nil, err
	}
	if refs == nil {
		return nil, nil
	}
	return refNames(refs), nil
}

func (r *Registry) listRefs(entityType string) ([]backendRef, error) {
	if r == nil {
		return nil, nil
	}
	node, ok := r.raw[entityType]
	if !ok {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("registry: connector.%s must be a list of backend binary names, got %s", entityType, nodeKindName(node.Kind))
	}
	return decodeBackendRefs("connector."+entityType, node.Content)
}

// Single returns the one name registered under connector.<entityType>,
// which must decode as a YAML scalar or one {name, command} mapping (scm
// today). An entityType with no entry returns ("", nil).
func (r *Registry) Single(entityType string) (string, error) {
	ref, ok, err := r.singleRef(entityType)
	if err != nil || !ok {
		return "", err
	}
	return ref.Name, nil
}

func (r *Registry) singleRef(entityType string) (backendRef, bool, error) {
	if r == nil {
		return backendRef{}, false, nil
	}
	node, ok := r.raw[entityType]
	if !ok {
		return backendRef{}, false, nil
	}
	if node.Kind != yaml.ScalarNode && node.Kind != yaml.MappingNode {
		return backendRef{}, false, fmt.Errorf("registry: connector.%s must be a single backend binary name, got %s", entityType, nodeKindName(node.Kind))
	}
	ref, err := decodeBackendRef("connector."+entityType, -1, &node)
	if err != nil {
		return backendRef{}, false, err
	}
	return ref, true, nil
}

// BackendConfig returns the opaque config block registered under the
// top-level backends.<name> key (bead pg2-2j5ac.28.1), as raw
// JSON — nil (not an error) when no such block exists, matching every
// other Registry accessor's "absent key -> zero value, not an error"
// convention (List/Single's own doc comments). The umbrella (Invoke)
// never parses this block's own contents — the returned bytes are copied
// VERBATIM into every wire request sent to name.
func (r *Registry) BackendConfig(name string) (json.RawMessage, error) {
	if r == nil {
		return nil, nil
	}
	node, ok := r.backends[name]
	if !ok {
		return nil, nil
	}
	var v any
	if err := node.Decode(&v); err != nil {
		return nil, fmt.Errorf("registry: backends.%s: %w", name, err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("registry: backends.%s: encode as JSON: %w", name, err)
	}
	return raw, nil
}

// BackendQueryNames returns the sorted list of query names name's own
// backends.<name>.queries block defines, without resolving any of them —
// used by "config validate"/"config show --queries" (config_validate.go,
// config_show.go) to report what a backend declares, never invoked
// against the backend itself. A backend with no registered config block,
// or a config block with no queries key at all, returns (nil, nil) — the
// ordinary case for a backend that doesn't implement "list" at all (ci,
// scm) or one that simply defines no named queries yet.
func (r *Registry) BackendQueryNames(name string) ([]string, error) {
	config, err := r.BackendConfig(name)
	if err != nil {
		return nil, err
	}
	if len(config) == 0 {
		return nil, nil
	}
	var parsed struct {
		Queries map[string]json.RawMessage `json:"queries"`
	}
	if err := json.Unmarshal(config, &parsed); err != nil {
		return nil, fmt.Errorf("registry: backends.%s: %w", name, err)
	}
	if len(parsed.Queries) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(parsed.Queries))
	for n := range parsed.Queries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// StateValue returns the string value of a scalar key under the top-level
// state: block (e.g. "consumer_prune_after"), matching the shared config
// file's state: block already rendered by home/programs/pg-connector's
// state option (an arbitrary attrset rendered verbatim — see this file's
// registryDoc doc comment). Returns ("", false) when the state: block is
// absent, key is absent, or the key's value is not a scalar — callers
// apply their own default in every one of those cases, matching every
// other Registry accessor's "absent -> zero value, not an error"
// convention (List/Single/BackendConfig's own doc comments).
func (r *Registry) StateValue(key string) (string, bool) {
	if r == nil {
		return "", false
	}
	node, ok := r.state[key]
	if !ok {
		return "", false
	}
	if node.Kind != yaml.ScalarNode {
		return "", false
	}
	var v string
	if err := node.Decode(&v); err != nil {
		return "", false
	}
	return v, true
}

// AttentionSources returns the backend names (bare binary names or
// {name, command} instance names) registered under the
// top-level attention.sources key, decoded as a YAML list, independent of
// connector.<type>. It applies the same validation List already applies to
// connector.<type> entries (non-empty bare names, no path separator, no
// in-list duplicates) but no cross-check against connector.<type>'s own
// registrations — a backend name may legitimately appear under both
// (e.g. a backend implementing list_attention alongside its normal
// entity-type ops in the same binary). An absent attention key — or an
// attention: mapping present without a nested sources: sub-key, e.g. a
// typo'd attention.souce — returns (nil, nil), matching List's own
// no-entry convention; that typo case is deliberately left un-tightened
// (see this file's registryDoc/sourcesDoc doc comments).
func (r *Registry) AttentionSources() ([]string, error) {
	if r == nil {
		return nil, nil
	}
	return sourcesList("attention.sources", r.attentionSources)
}

// SearchSources is AttentionSources's counterpart for the top-level
// search.sources key.
func (r *Registry) SearchSources() ([]string, error) {
	if r == nil {
		return nil, nil
	}
	return sourcesList("search.sources", r.searchSources)
}

// ActivitySources is AttentionSources's counterpart for the top-level
// activity.sources key: the backend names (bare binary names or
// {name, command} instance names) invoked by the activity list
// verb and by nothing else (they are not part of AllBackends). Absent
// activity key (or nested sources: key) returns (nil, nil); an explicit
// sources: [] is rejected. A capability-only backend (any binary named
// pg-connector-activity-*, e.g. pg-connector-activity-git) MUST appear only
// here: parseRegistry rejects it under any connector.<type> key at load time,
// so a registry that lists it there never loads. Other binaries MAY also
// appear under connector.<type>; no cross-check is made for them.
func (r *Registry) ActivitySources() ([]string, error) {
	if r == nil {
		return nil, nil
	}
	return sourcesList("activity.sources", r.activitySources)
}

// sourcesList is the shared validation behind AttentionSources,
// SearchSources and ActivitySources. sources == nil means the key (or its nested sources:
// sub-key) was absent, returning (nil, nil) unvalidated; a non-nil slice —
// including a non-nil empty one from an explicit sources: [] — is
// decoded with decodeBackendRefs, so an explicitly-present but empty
// list is rejected rather than silently treated the same as absent.
func sourcesList(key string, sources []yaml.Node) ([]string, error) {
	if sources == nil {
		return nil, nil
	}
	refs, err := decodeBackendRefs(key, nodePtrs(sources))
	if err != nil {
		return nil, err
	}
	return refNames(refs), nil
}

// entityTypes enumerates every connector.<type> key this docket's design
// names, so a fan-out can walk "every registered backend regardless of
// capability." This docket now populates all six — pr, issue, ci, scm,
// thread, and calendar — via their own Tier-2 backends, and the registry
// stays generic over the full set. "thread" was appended by bead
// pg2-2j5ac.40.3 (Phase 13's Thread v1 schema + claude -p backend packet);
// "calendar" was appended by this docket's own CLI verb group packet
// (pg2-o2dmu.3) — both list-valued like pr/issue/ci, never single-valued
// like scm (matching pg2-2j5ac.40.3's own precedent, same file, same
// list). "agentsession" was appended by the agentsession connector
// docket's own Tier-1 CLI verb group packet (pg2-eezd1.7) — same
// list-valued precedent as thread/calendar's own additions above. "mail"
// was appended by the mail Tier-2 docket's own CLI verb group packet
// (pg2-qc5uc.8) — a NEW capability (phillipgreenii-nix-agent-support ADR
// 0062, "Decision" item 10), list-valued like thread/calendar, never
// single-valued like scm.
var entityTypes = []string{"pr", "issue", "ci", "scm", "thread", "calendar", "agentsession", "alert", "mail"}

// AllBackends returns every backend name registered under any
// connector.<type> entry, across both list-valued and single-valued types.
// A name registered under more than one type — a multi-capability
// backend, mandatory per INV-REG-1 — is deduplicated to exactly one
// entry, in first-occurrence order (entityTypes' fixed pr/issue/ci/scm
// order), rather than producing one sources[] row per type it appears
// under [bug A27]. Two instances of one binary are two entries.
func (r *Registry) AllBackends() ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	for _, t := range entityTypes {
		refs, err := r.entityRefs(t)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			if seen[ref.Name] {
				continue
			}
			seen[ref.Name] = true
			out = append(out, ref.Name)
		}
	}
	return out, nil
}

// entityRefs decodes whatever connector.<t> holds (list, scalar or one
// mapping) into its registrations; an absent type returns nil.
func (r *Registry) entityRefs(t string) ([]backendRef, error) {
	if r == nil {
		return nil, nil
	}
	node, ok := r.raw[t]
	if !ok {
		return nil, nil
	}
	switch node.Kind {
	case yaml.SequenceNode:
		return r.listRefs(t)
	case yaml.ScalarNode, yaml.MappingNode:
		ref, ok, err := r.singleRef(t)
		if err != nil || !ok {
			return nil, err
		}
		return []backendRef{ref}, nil
	default:
		return nil, fmt.Errorf("registry: connector.%s must be a list or a single backend binary name, got %s", t, nodeKindName(node.Kind))
	}
}

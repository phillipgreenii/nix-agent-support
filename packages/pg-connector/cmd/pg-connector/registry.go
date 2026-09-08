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
// key, either a list of backend binary names (issue/ci/pr today) or a
// single backend binary name (scm today). Every registry value is a bare
// binary name — there is no exec:-prefix distinction anywhere in this
// registry, since nothing is compiled in.
//
// attentionSources/searchSources hold the top-level attention.sources /
// search.sources registrations — always list-valued, and independent of
// connector.<type> (never read from raw).
// nil means the key (or its nested sources: sub-key) was absent; a non-nil
// slice — including a non-nil empty one from an explicit sources: [] —
// came from an actual sources: entry. That absent-vs-empty distinction is
// exactly what AttentionSources/SearchSources need and is preserved
// unchanged from how parseRegistry decodes it.
type Registry struct {
	raw              map[string]yaml.Node
	attentionSources []string
	searchSources    []string
}

type registryDoc struct {
	Connector map[string]yaml.Node `yaml:"connector"`
	Attention *sourcesDoc          `yaml:"attention"`
	Search    *sourcesDoc          `yaml:"search"`
}

// sourcesDoc is the shape of the top-level attention:/search: mappings: a
// mapping with a single nested sources: list key, siblings of connector:
// rather than members of it.
type sourcesDoc struct {
	Sources []string `yaml:"sources"`
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
	reg := &Registry{raw: doc.Connector}
	if doc.Attention != nil {
		reg.attentionSources = doc.Attention.Sources
	}
	if doc.Search != nil {
		reg.searchSources = doc.Search.Sources
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

// validateBackendList applies the shared list-valued registration rules —
// reject an explicitly-empty list, reject an invalid bare name, reject a
// duplicate name within the same list — shared by both connector.<type>
// (List) and the always-list-valued attention.sources/search.sources. key
// names the specific registry key the list came from (e.g. "connector.pr",
// "attention.sources"), used verbatim in error messages.
func validateBackendList(key string, out []string) error {
	if len(out) == 0 {
		return fmt.Errorf("registry: %s is an empty list; omit the key entirely if no backend should be registered for %s", key, key)
	}
	seen := make(map[string]bool, len(out))
	for _, name := range out {
		if err := validateBackendName(key, name); err != nil {
			return err
		}
		if seen[name] {
			return fmt.Errorf("registry: %s: duplicate backend name %q", key, name)
		}
		seen[name] = true
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
	default:
		return "an unrecognized YAML node"
	}
}

// List returns the bare binary names registered under
// connector.<entityType>, which must decode as a YAML list (pr/issue/ci
// today). An entityType with no entry returns (nil, nil).
func (r *Registry) List(entityType string) ([]string, error) {
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
	var out []string
	if err := node.Decode(&out); err != nil {
		return nil, fmt.Errorf("registry: connector.%s: %w", entityType, err)
	}
	if err := validateBackendList("connector."+entityType, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Single returns the one bare binary name registered under
// connector.<entityType>, which must decode as a YAML scalar (scm today).
// An entityType with no entry returns ("", nil).
func (r *Registry) Single(entityType string) (string, error) {
	if r == nil {
		return "", nil
	}
	node, ok := r.raw[entityType]
	if !ok {
		return "", nil
	}
	if node.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("registry: connector.%s must be a single backend binary name, got %s", entityType, nodeKindName(node.Kind))
	}
	var out string
	if err := node.Decode(&out); err != nil {
		return "", fmt.Errorf("registry: connector.%s: %w", entityType, err)
	}
	if err := validateBackendName("connector."+entityType, out); err != nil {
		return "", err
	}
	return out, nil
}

// AttentionSources returns the bare binary names registered under the
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

// sourcesList is the shared validation behind AttentionSources and
// SearchSources. sources == nil means the key (or its nested sources:
// sub-key) was absent, returning (nil, nil) unvalidated; a non-nil slice —
// including a non-nil empty one from an explicit sources: [] — is
// validated with validateBackendList, so an explicitly-present but empty
// list is rejected rather than silently treated the same as absent.
func sourcesList(key string, sources []string) ([]string, error) {
	if sources == nil {
		return nil, nil
	}
	if err := validateBackendList(key, sources); err != nil {
		return nil, err
	}
	return sources, nil
}

// entityTypes enumerates every connector.<type> key this docket's design
// names, so a fan-out can walk "every registered backend regardless of
// capability." This docket now populates all four — pr, issue, ci, and
// scm — via their own Tier-2 backends, and the registry stays generic
// over the full set.
var entityTypes = []string{"pr", "issue", "ci", "scm"}

// AllBackends returns every backend binary name registered under any
// connector.<type> entry, across both list-valued and single-valued types.
// A binary registered under more than one type — a multi-capability
// backend, mandatory per INV-REG-1 — is deduplicated to exactly one
// entry, in first-occurrence order (entityTypes' fixed pr/issue/ci/scm
// order), rather than producing one sources[] row per type it appears
// under [bug A27].
func (r *Registry) AllBackends() ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	for _, t := range entityTypes {
		node, ok := r.raw[t]
		if !ok {
			continue
		}
		var names []string
		switch node.Kind {
		case yaml.SequenceNode:
			list, err := r.List(t)
			if err != nil {
				return nil, err
			}
			names = list
		case yaml.ScalarNode:
			single, err := r.Single(t)
			if err != nil {
				return nil, err
			}
			if single != "" {
				names = []string{single}
			}
		default:
			return nil, fmt.Errorf("registry: connector.%s must be a list or a single backend binary name, got %s", t, nodeKindName(node.Kind))
		}
		for _, name := range names {
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

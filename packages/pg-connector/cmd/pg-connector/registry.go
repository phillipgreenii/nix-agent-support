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
type Registry struct {
	raw map[string]yaml.Node
}

type registryDoc struct {
	Connector map[string]yaml.Node `yaml:"connector"`
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

func loadRegistryFromEnv(env envSource) (*Registry, error) {
	if explicit := env.Getenv("PG_PR_CONFIG"); explicit != "" {
		reg, err := loadRegistryFile(explicit)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("registry: $PG_PR_CONFIG=%s does not exist", explicit)
			}
			return nil, err
		}
		return reg, nil
	}

	candidates := registryCandidates(env)
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return loadRegistryFile(p)
		}
	}

	return nil, fmt.Errorf("%w: looked in %s; create one or set $PG_PR_CONFIG",
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
	dec.KnownFields(false)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("registry: parse %s: %w", path, err)
	}
	return &Registry{raw: doc.Connector}, nil
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
	return out, nil
}

// entityTypes enumerates every connector.<type> key this docket's design
// names, so a fan-out can walk "every registered backend regardless of
// capability." This docket only ever populates pr, but the registry stays
// generic over the full set.
var entityTypes = []string{"pr", "issue", "ci", "scm"}

// AllBackends returns every backend binary name registered under any
// connector.<type> entry, across both list-valued and single-valued types.
func (r *Registry) AllBackends() ([]string, error) {
	var out []string
	for _, t := range entityTypes {
		node, ok := r.raw[t]
		if !ok {
			continue
		}
		switch node.Kind {
		case yaml.SequenceNode:
			list, err := r.List(t)
			if err != nil {
				return nil, err
			}
			out = append(out, list...)
		case yaml.ScalarNode:
			single, err := r.Single(t)
			if err != nil {
				return nil, err
			}
			if single != "" {
				out = append(out, single)
			}
		default:
			return nil, fmt.Errorf("registry: connector.%s must be a list or a single backend binary name, got %s", t, nodeKindName(node.Kind))
		}
	}
	return out, nil
}

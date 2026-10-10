// registry_backend.go: the registry entry shape (bead pg2-91y12, ADR 0062
// amendment, INV-REG-4). Every registration — each connector.<type> entry,
// attention.sources, search.sources, activity.sources — is either a plain
// string (name and binary are the same word, command is [name], exactly as
// before) or a mapping {name, command}: an INSTANCE whose name is the
// backend's identity everywhere the umbrella uses one (sources[] rows, the
// --backend pin, backends.<name>, cache and ledger keys) and whose command is
// an argv LIST (never a shell string) run as command[0] with command[1:] as
// its arguments. One backend binary may therefore be registered more than
// once under different names with different arguments.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// backendRef is one parsed registration.
type backendRef struct {
	// Name is the instance identity: sources[].source, the --backend pin,
	// the backends.<Name> key, and the cache and ledger key.
	Name string
	// Command is the argv: Command[0] the bare binary, Command[1:] its
	// arguments. Always non-empty after a successful decode; a plain
	// string registration is []string{Name}.
	Command []string
	// Explicit is true when written as {name, command}; config show lists
	// only these under "commands".
	Explicit bool
}

// cacheKeySeparator is the two-character separator cache.go and ledger.go
// join key parts with; a registered name containing it would corrupt both
// parsers, so validateRefName forbids it.
const cacheKeySeparator = "__"

// validateRefName applies the name rules shared by plain strings and the
// name: of a mapping: the existing validateBackendName rules (non-empty, no
// path separator) plus the new no-"__" rule.
func validateRefName(key, name string) error {
	if err := validateBackendName(key, name); err != nil {
		return err
	}
	if strings.Contains(name, cacheKeySeparator) {
		return fmt.Errorf("registry: %s: backend name %q must not contain %q (it is the cache and ledger key separator)", key, name, cacheKeySeparator)
	}
	return nil
}

// validateCommandWord rejects a command[0] that is not a bare binary name:
// empty, containing a path separator, or containing whitespace (the shell
// string mistake in list clothing, e.g. ["bin --flag x"]).
func validateCommandWord(key, name, word string) error {
	if word == "" ||
		strings.ContainsRune(word, '/') || strings.ContainsRune(word, filepath.Separator) ||
		strings.IndexFunc(word, unicode.IsSpace) >= 0 {
		return fmt.Errorf("registry: %s: backend %q: command[0] %q must be a bare binary name (non-empty, no path separator, no whitespace); put arguments in later list elements", key, name, word)
	}
	return nil
}

// decodeBackendRef decodes one registration entry n found at index idx of
// the list (or the single value, idx < 0) registered under key.
func decodeBackendRef(key string, idx int, n *yaml.Node) (backendRef, error) {
	where := key
	if idx >= 0 {
		where = fmt.Sprintf("%s[%d]", key, idx)
	}
	switch n.Kind {
	case yaml.ScalarNode:
		var name string
		if err := n.Decode(&name); err != nil {
			return backendRef{}, fmt.Errorf("registry: %s: %w", key, err)
		}
		if err := validateRefName(key, name); err != nil {
			return backendRef{}, err
		}
		return backendRef{Name: name, Command: []string{name}}, nil
	case yaml.MappingNode:
		return decodeInstanceMapping(key, where, n)
	default:
		return backendRef{}, fmt.Errorf("registry: %s: a backend must be a bare binary name or a {name, command} mapping, got %s", where, nodeKindName(n.Kind))
	}
}

func decodeInstanceMapping(key, where string, n *yaml.Node) (backendRef, error) {
	var nameNode, commandNode *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch {
		case k.Kind == yaml.ScalarNode && k.Value == "name" && nameNode == nil:
			nameNode = v
		case k.Kind == yaml.ScalarNode && k.Value == "command" && commandNode == nil:
			commandNode = v
		default:
			return backendRef{}, fmt.Errorf("registry: %s: unknown or duplicate key %q in a backend entry; a {name, command} entry has exactly those two keys", where, k.Value)
		}
	}
	if nameNode == nil {
		return backendRef{}, fmt.Errorf("registry: %s: backend entry must have a name", where)
	}
	if nameNode.Kind != yaml.ScalarNode {
		return backendRef{}, fmt.Errorf("registry: %s: backend name must be a string, got %s", where, nodeKindName(nameNode.Kind))
	}
	name := nameNode.Value
	if err := validateRefName(key, name); err != nil {
		return backendRef{}, err
	}
	if commandNode == nil {
		return backendRef{}, fmt.Errorf("registry: %s: backend %q: command is required (an argv list such as [binary, --flag, value])", key, name)
	}
	if commandNode.Kind != yaml.SequenceNode {
		return backendRef{}, fmt.Errorf("registry: %s: backend %q: command must be a list of strings (an argv list), not %s; a shell string is not accepted", key, name, nodeKindName(commandNode.Kind))
	}
	if len(commandNode.Content) == 0 {
		return backendRef{}, fmt.Errorf("registry: %s: backend %q: command must not be an empty list", key, name)
	}
	command := make([]string, 0, len(commandNode.Content))
	for j, w := range commandNode.Content {
		if w.Kind != yaml.ScalarNode || w.Tag == "!!null" {
			return backendRef{}, fmt.Errorf("registry: %s: backend %q: command[%d] must be a string", key, name, j)
		}
		command = append(command, w.Value)
	}
	if err := validateCommandWord(key, name, command[0]); err != nil {
		return backendRef{}, err
	}
	return backendRef{Name: name, Command: command, Explicit: true}, nil
}

// decodeBackendRefs decodes a registration list: an explicitly empty list is
// rejected, and so is a duplicate name within it (same texts as before the
// instance shape existed).
func decodeBackendRefs(key string, entries []*yaml.Node) ([]backendRef, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("registry: %s is an empty list; omit the key entirely if no backend should be registered for %s", key, key)
	}
	out := make([]backendRef, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for i, n := range entries {
		ref, err := decodeBackendRef(key, i, n)
		if err != nil {
			return nil, err
		}
		if seen[ref.Name] {
			return nil, fmt.Errorf("registry: %s: duplicate backend name %q", key, ref.Name)
		}
		seen[ref.Name] = true
		out = append(out, ref)
	}
	return out, nil
}

// nodePtrs returns pointers to the elements of nodes (so decoding does not
// copy yaml.Node values).
func nodePtrs(nodes []yaml.Node) []*yaml.Node {
	out := make([]*yaml.Node, len(nodes))
	for i := range nodes {
		out[i] = &nodes[i]
	}
	return out
}

func refNames(refs []backendRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Name
	}
	return out
}

// registration is one successfully decoded registration key, in parse order.
type registration struct {
	key  string
	refs []backendRef
}

// lenientRefs decodes entries, skipping every entry that does not decode:
// the accessors report shape errors themselves, so the parse-time pass
// (cross-registration conflicts, capability-only guard) only reasons about
// entries that are individually valid.
func lenientRefs(key string, entries []*yaml.Node) []backendRef {
	var out []backendRef
	seen := make(map[string]bool, len(entries))
	for i, n := range entries {
		ref, err := decodeBackendRef(key, i, n)
		if err != nil || seen[ref.Name] {
			// A duplicate within one list is the accessor's error to
			// report (decodeBackendRefs), not a cross-registration conflict.
			continue
		}
		seen[ref.Name] = true
		out = append(out, ref)
	}
	return out
}

// connectorEntries returns the entry nodes of connector.<t>: a sequence's
// elements, or the node itself for a scalar/mapping (scm's single value).
func connectorEntries(node *yaml.Node) []*yaml.Node {
	if node.Kind == yaml.SequenceNode {
		return node.Content
	}
	return []*yaml.Node{node}
}

// registrations lists every registration key in a fixed order (connector
// entity types in entityTypes order, then attention, search, activity).
func (r *Registry) registrations() []registration {
	var out []registration
	for _, t := range entityTypes {
		node, ok := r.raw[t]
		if !ok {
			continue
		}
		key := "connector." + t
		out = append(out, registration{key, lenientRefs(key, connectorEntries(&node))})
	}
	for _, s := range []struct {
		key   string
		nodes []yaml.Node
	}{
		{"attention.sources", r.attentionSources},
		{"search.sources", r.searchSources},
		{"activity.sources", r.activitySources},
	} {
		if s.nodes != nil {
			out = append(out, registration{s.key, lenientRefs(s.key, nodePtrs(s.nodes))})
		}
	}
	return out
}

// buildCommands records name -> argv over every registration and rejects a
// name registered with two different commands (a plain string is
// {name, [name]}, so forgetting the command on one list is caught loudly).
func buildCommands(regs []registration) (map[string][]string, error) {
	commands := make(map[string][]string)
	firstKey := make(map[string]string)
	for _, reg := range regs {
		for _, ref := range reg.refs {
			prev, seen := commands[ref.Name]
			if !seen {
				commands[ref.Name] = ref.Command
				firstKey[ref.Name] = reg.key
				continue
			}
			if !sameCommand(prev, ref.Command) {
				return nil, fmt.Errorf("registry: backend %q is registered with different commands (%s: %q vs %s: %q); a name registered more than once must carry the same command each time",
					ref.Name, firstKey[ref.Name], prev, reg.key, ref.Command)
			}
		}
	}
	return commands, nil
}

func sameCommand(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Command returns the argv registered for name: the explicit command of an
// instance, otherwise []string{name} (a plain string, an unknown name, or a
// nil registry). Never returns an empty slice.
func (r *Registry) Command(name string) []string {
	if r != nil {
		if c, ok := r.commands[name]; ok {
			return c
		}
	}
	return []string{name}
}

// Target is the scriptout.Target for the registered name.
func (r *Registry) Target(name string) scriptout.Target {
	return scriptout.Target{Name: name, Command: r.Command(name)}
}

// Invoke runs op against the registered backend name with its registered
// argv. Every umbrella exec goes through it (or InvokeCapabilities) so a
// capability probe can never run an instance without its flag.
func (r *Registry) Invoke(ctx context.Context, name, op string, args any, config json.RawMessage) (*scriptout.Response, error) {
	return scriptout.InvokeTarget(ctx, r.Target(name), op, args, config)
}

// InvokeCapabilities runs the capabilities op against the registered backend
// name with its registered argv.
func (r *Registry) InvokeCapabilities(ctx context.Context, name string) (*scriptout.CapabilitiesResponse, error) {
	return scriptout.InvokeCapabilitiesTarget(ctx, r.Target(name))
}

// Explicit returns name -> argv for every instance written as
// {name, command}, which config show lists under "commands".
func (r *Registry) Explicit() map[string][]string {
	if r == nil {
		return nil
	}
	out := make(map[string][]string)
	for _, reg := range r.registrations() {
		for _, ref := range reg.refs {
			if ref.Explicit {
				out[ref.Name] = ref.Command
			}
		}
	}
	return out
}

// Validate runs every accessor and returns the first error, so a malformed
// entry under attention/search/activity (never exercised by the connector
// fan-out) is reported by config validate and config show instead of passing
// silently. It never execs a backend.
func (r *Registry) Validate() error {
	if r == nil {
		return nil
	}
	types := make([]string, 0, len(r.raw))
	for _, t := range entityTypes {
		if _, ok := r.raw[t]; ok {
			types = append(types, t)
		}
	}
	for _, t := range types {
		if _, err := r.entityRefs(t); err != nil {
			return err
		}
	}
	if _, err := r.AttentionSources(); err != nil {
		return err
	}
	if _, err := r.SearchSources(); err != nil {
		return err
	}
	if _, err := r.ActivitySources(); err != nil {
		return err
	}
	_, err := buildCommands(r.registrations())
	return err
}

// sortedKeys returns m's keys in order.
func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

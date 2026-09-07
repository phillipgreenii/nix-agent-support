package cmddesc

import "sort"

// Registry maps a command basename to its schema value. It is the ONLY place
// a command name appears in the spike: adding a command is adding an entry.
type Registry struct {
	schemas map[string]CommandSchema
}

// NewRegistry builds a registry from schema values, keyed by Name.
func NewRegistry(schemas ...CommandSchema) Registry {
	r := Registry{schemas: make(map[string]CommandSchema, len(schemas))}
	for _, s := range schemas {
		r.schemas[s.Name] = s
	}
	return r
}

// Lookup returns the schema for a basename.
func (r Registry) Lookup(basename string) (CommandSchema, bool) {
	s, ok := r.schemas[basename]
	return s, ok
}

// Names lists the registered basenames, sorted.
func (r Registry) Names() []string {
	names := make([]string, 0, len(r.schemas))
	for n := range r.schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DefaultRegistry returns the spike's registry: cat and head as plain values.
// head is the proof that a second command is ONLY a registry entry.
func DefaultRegistry() Registry {
	return NewRegistry(catSchema, headSchema)
}

// inert is the arity-0, transform-free flag spec.
var inert = FlagSpec{}

var catSchema = CommandSchema{
	Name:       "cat",
	Provenance: "GNU coreutils 9.x cat --help",
	Flags: map[string]FlagSpec{
		"-n": inert, "--number": inert,
		"-b": inert, "--number-nonblank": inert,
		"-A": inert, "--show-all": inert,
		"-E": inert, "--show-ends": inert,
		"-T": inert, "--show-tabs": inert,
		"-s": inert, "--squeeze-blank": inert,
		"-v": inert, "--show-nonprinting": inert,
		"-e": inert, "-t": inert, "-u": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead, StdinToken: "-"},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

var headSchema = CommandSchema{
	Name:       "head",
	Provenance: "GNU coreutils 9.x head --help",
	Flags: map[string]FlagSpec{
		"-n": {Arity: 1, Operand: Literal}, "--lines": {Arity: 1, Operand: Literal},
		"-c": {Arity: 1, Operand: Literal}, "--bytes": {Arity: 1, Operand: Literal},
		"-q": inert, "--quiet": inert,
		"-v": inert, "--verbose": inert,
	},
	Positionals:  PositionalSpec{Rest: PathRead, StdinToken: "-"},
	Stdin:        StdinWhenNoPathOperands,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

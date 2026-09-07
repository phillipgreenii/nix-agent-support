package cmddesc

// ProgramInterpretation is what a DialectInterpreter produces for one program
// operand: the effects it could classify, any child invocations, and whether
// the classification was SUFFICIENT (every construct understood). An
// insufficient result still carries the effects it did understand, but the
// enclosing interpretation can never be approved.
type ProgramInterpretation struct {
	Effects       []Effect
	Children      []ChildInvocation
	Sufficient    bool
	Insufficiency string
}

// DialectInterpreter classifies program text written in one dialect (a sed
// script, an awk program) into effects. It is the seam that stops a Program
// operand from being an unjudged opaque blob: the generic interpreter looks
// the operand's dialect up here and folds the result's sufficiency into its
// own, so a dialect with no interpreter — or a construct the interpreter
// cannot classify — lands on insufficient, never on approve.
type DialectInterpreter interface {
	InterpretProgram(program string, ctx Context) ProgramInterpretation
}

// dialects is the lookup table keyed by dialect name. Adding a dialect is
// adding an entry. "shell" and "bash" name the same interpreter: the program
// is handed back as a child for the graph builder to parse and recurse into.
var dialects = map[string]DialectInterpreter{
	"sed":   sedDialect{},
	"shell": shellDialect{},
	"bash":  shellDialect{},
}

// LookupDialect resolves a dialect name. An unknown name reports false so the
// caller marks the program insufficient rather than guessing.
func LookupDialect(name string) (DialectInterpreter, bool) {
	d, ok := dialects[name]
	return d, ok
}

// RegisterDialect adds (or replaces) a dialect interpreter under name. It
// exists for tests and extension; it is not safe to call concurrently with
// interpretation.
func RegisterDialect(name string, d DialectInterpreter) {
	dialects[name] = d
}

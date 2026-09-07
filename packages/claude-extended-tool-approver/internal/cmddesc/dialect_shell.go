package cmddesc

// shellDialect is the "shell"/"bash" dialect: the program is NOT classified
// here but handed back as one ChildInvocation for the graph builder to parse
// and recurse into, so its leaves become nodes the policies judge like any
// other. The result is sufficient because the CHILD is judged by recursion,
// not by the parent — an unparseable or unmodeled child makes the child (and
// the parent, through the builder) insufficient, never this dialect.
type shellDialect struct{}

// InterpretProgram implements DialectInterpreter.
func (shellDialect) InterpretProgram(program string, _ Context) ProgramInterpretation {
	return ProgramInterpretation{
		Children:   []ChildInvocation{{Dialect: "shell", Program: program}},
		Sufficient: true,
	}
}

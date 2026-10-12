package cmddesc

import (
	"fmt"
	"regexp"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// yqLoadCall finds a call of a yq file-loading operator in an expression.
// Facts (P14), verified against yq (mikefarah) v4.53.2 by running
// `yq -n 'OP("x.yml")'` on a scratch file: `load`, `strload`, `load_str`,
// `load_props` and `load_xml`/`load_base64` (the last two errored only on the
// file's CONTENT format, i.e. they opened it) each read the named file; and
// `yq eval --help` documents `--security-disable-file-ops` as "Disable file
// related operations (e.g. load)". Anything named below is therefore a file
// read of its argument.
var yqLoadCall = regexp.MustCompile(`\b(load|strload|load_str|load_xml|load_props|load_base64)\s*\(`)

// yqLoadLiteralArg matches a call whose whole argument is one double-quoted
// string without escapes, e.g. load("x.yml").
var yqLoadLiteralArg = regexp.MustCompile(`^\s*"([^"\\]*)"\s*\)`)

// yqInterpreter runs the generic scan and then adds one PathRead effect per
// file-loading operator call with a literal path in the expression
// (positional or --expression value). A load-family call whose argument is
// not a plain string literal (a variable, concatenation, escape) names a path
// the engine cannot resolve and is insufficient (P3). Phase 2f, tc-o14i5.3.6.
type yqInterpreter struct{}

// Interpret implements Interpreter.
func (yqInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st := scan(leaf, schema, ctx)
	st.finish()
	if !st.scanned {
		return st.result()
	}
	var exprs []pendingOp
	exprs = append(exprs, st.flagValues("--expression")...)
	if !st.anyFlagSeen("--from-file", "--expression") {
		if ps := st.positionals(); len(ps) > 0 {
			exprs = append(exprs, ps[0])
		}
	}
	for _, e := range exprs {
		if yqLoadCall.FindStringIndex(e.tok) == nil {
			continue
		}
		if st.leaf.ArgIsLiveExpansion(e.idx) {
			st.fail("yq expression at arg %d is a runtime expansion containing a file-loading operator", e.idx)
			continue
		}
		for _, loc := range yqLoadCall.FindAllStringIndex(e.tok, -1) {
			m := yqLoadLiteralArg.FindStringSubmatch(e.tok[loc[1]:])
			if m == nil {
				st.fail("yq file-loading operator at arg %d has a non-literal path argument", e.idx)
				continue
			}
			st.effects = append(st.effects, Effect{Kind: EffectPath, Path: m[1], Access: AccessRead, Source: fmt.Sprintf("arg %d", e.idx), Detail: "yq load operator"})
		}
	}
	return st.result()
}

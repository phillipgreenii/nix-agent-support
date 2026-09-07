package cmddesc

import (
	"reflect"
	"strings"
	"testing"
)

func TestSedDialect(t *testing.T) {
	read := func(p, src string) Effect {
		return Effect{Kind: EffectPath, Path: p, Access: AccessRead, Source: src}
	}
	write := func(p, src string) Effect {
		return Effect{Kind: EffectPath, Path: p, Access: AccessTruncate, Source: src}
	}
	shell := func(prog, src string) Effect {
		return Effect{Kind: EffectProgram, Program: prog, Dialect: "shell", Source: src}
	}
	cases := []struct {
		name       string
		script     string
		sufficient bool
		effects    []Effect
		reason     string // substring of Insufficiency when !sufficient
	}{
		// Inert scripts.
		{"substitute", "s/a/b/", true, nil, ""},
		{"substitute with flags", "s/a/b/gI2", true, nil, ""},
		{"custom delimiter", "s|/usr|/opt|g", true, nil, ""},
		{"delimiter inside bracket", "s/[/]/X/", true, nil, ""},
		{"character class", "s/[[:space:]]*$//", true, nil, ""},
		{"escaped delimiter", `s/a\/b/c/`, true, nil, ""},
		{"print with -n style", "1p", true, nil, ""},
		{"regex address", "/foo/d", true, nil, ""},
		{"regex address with bracket delim", "/[/]/p", true, nil, ""},
		{"range address", "1,$p", true, nil, ""},
		{"step address", "0~2d", true, nil, ""},
		{"negated address", "/x/!d", true, nil, ""},
		{"custom regex delimiter address", `\%x%p`, true, nil, ""},
		{"semicolon separated", "s/a/b/;p;d", true, nil, ""},
		{"newline separated", "s/a/b/\np\n", true, nil, ""},
		{"braces", "/x/{s/a/b/;p}", true, nil, ""},
		{"labels and branches", ":a;N;$!ba;s/\\n/ /g", true, nil, ""},
		{"branch with label then command", "b end; p; :end", true, nil, ""},
		{"transliterate", "y/abc/xyz/", true, nil, ""},
		{"quit with code", "q5", true, nil, ""},
		{"l with length", "l 5;p", true, nil, ""},
		{"append one-liner", "1a hello world", true, nil, ""},
		{"append classic", "1a\\\nhello\\\nworld\np", true, nil, ""},
		{"insert and change", "1i x\n2c y", true, nil, ""},
		{"comment", "# comment\np", true, nil, ""},
		{"many inert commands", "n;N;g;G;h;H;x;=;D;P;z;F;Q", true, nil, ""},

		// File reads.
		{"r reads", "r /etc/passwd", true, []Effect{read("/etc/passwd", "sed r")}, ""},
		{"R reads", "1R in.txt", true, []Effect{read("in.txt", "sed R")}, ""},

		// File writes.
		{"w writes", "w /nix/store/out", true, []Effect{write("/nix/store/out", "sed w")}, ""},
		{"W writes", "/x/W out.txt", true, []Effect{write("out.txt", "sed W")}, ""},
		{"s///w writes", "s/a/b/w /nix/store/out", true, []Effect{write("/nix/store/out", "sed s///w")}, ""},
		{"s///gw writes", "s/a/b/gw out", true, []Effect{write("out", "sed s///w")}, ""},
		{"filename runs to end of line", "w out.txt; p\np", true, []Effect{write("out.txt; p", "sed w")}, ""},

		// Shell execution: recorded AND insufficient.
		{"e command", "e ls", false, []Effect{shell("ls", "sed e")}, "executes a shell command"},
		{"e bare", "e", false, []Effect{shell("", "sed e")}, "executes a shell command"},
		{"s///e", "s/a/b/e", false, []Effect{shell("<pattern space>", "sed s///e")}, "executes the pattern space"},

		// Ambiguous or malformed: must be insufficient.
		{"unknown command", "k", false, nil, "unrecognised sed command"},
		{"unknown s flag", "s/a/b/x", false, nil, "unknown s flag"},
		{"unterminated s", "s/a/b", false, nil, "unterminated"},
		{"w without filename", "w", false, nil, "without a filename"},
		{"unbalanced open brace", "/x/{p", false, nil, "unbalanced"},
		{"unbalanced close brace", "p}", false, nil, "unexpected }"},
		{"backslash in bracket", `s/[\/]/x/`, false, nil, "backslash inside bracket"},
		{"junk after command", "p foo", false, nil, "unexpected text"},
		{"v extension unmodeled", "v 4.2", false, nil, "unrecognised sed command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sedDialect{}.InterpretProgram(tc.script, Context{})
			if got.Sufficient != tc.sufficient {
				t.Fatalf("sufficient = %v (%q), want %v", got.Sufficient, got.Insufficiency, tc.sufficient)
			}
			if !tc.sufficient && (got.Insufficiency == "" || !strings.Contains(got.Insufficiency, tc.reason)) {
				t.Errorf("insufficiency = %q, want substring %q", got.Insufficiency, tc.reason)
			}
			if !reflect.DeepEqual(got.Effects, tc.effects) {
				t.Errorf("effects = %+v\nwant     %+v", got.Effects, tc.effects)
			}
		})
	}
}

// TestUnknownDialectFailsClosed: a Program operand whose dialect has no
// interpreter keeps its Program effect and makes the interpretation
// insufficient — it can never be approved by omission.
func TestUnknownDialectFailsClosed(t *testing.T) {
	s := CommandSchema{Name: "x", Positionals: PositionalSpec{Leading: []OperandRole{Program("nosuch")}, Rest: PathRead}}
	got := GenericInterpreter{}.Interpret(leaf(t, "x 'prog' a"), s, Context{})
	if got.Sufficient || !strings.Contains(got.Insufficiency, "no interpreter for dialect nosuch") {
		t.Fatalf("got %+v", got)
	}
	if len(got.Effects) == 0 || got.Effects[0].Kind != EffectProgram || got.Effects[0].Dialect != "nosuch" {
		t.Errorf("program effect missing: %+v", got.Effects)
	}
	if _, ok := LookupDialect("sed"); !ok {
		t.Error("sed dialect must be registered")
	}
}

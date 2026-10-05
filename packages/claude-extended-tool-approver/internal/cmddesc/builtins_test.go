package cmddesc

import (
	"strings"
	"testing"
)

func interpretBuiltin(t *testing.T, command string) Interpretation {
	t.Helper()
	l := leaf(t, command)
	schema, ok := DefaultRegistry().Lookup(l.Executable)
	if !ok {
		t.Fatalf("%s is not registered", l.Executable)
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatalf("no interpreter for %s", l.Executable)
	}
	return in.Interpret(l, schema, Context{})
}

// TestTestBracketOperators (pg2-dbrsg): the pure-query operators of `[` and
// `test` are sufficient and produce no effect; `-v`/`-R` (a variable NAME that
// may carry an arithmetic subscript) and an unknown operator are not.
func TestTestBracketOperators(t *testing.T) {
	sufficient := []string{
		`[ -z "$x" ]`, `[ -n "$x" ]`, `[ -d /tmp ]`, `[ -f f ]`, `[ -e f ]`, `[ -r f ]`, `[ -w f ]`,
		`[ -x f ]`, `[ -s f ]`, `[ -L f ]`, `[ -h f ]`, `[ -p f ]`, `[ -S f ]`, `[ -t 1 ]`,
		`[ "$a" -eq 1 ]`, `[ "$a" -ne 1 ]`, `[ "$a" -lt 1 ]`, `[ "$a" -le 1 ]`, `[ "$a" -gt 1 ]`, `[ "$a" -ge 1 ]`,
		`[ f -nt g ]`, `[ f -ot g ]`, `[ f -ef g ]`,
		`[ "$a" = "$b" ]`, `[ "$a" != "$b" ]`, `[ ! -e f ]`, `[ -n a -a -n b ]`, `[ -n a -o -n b ]`,
		`test -d f`, `test "$a" = b`,
	}
	for _, c := range sufficient {
		res := interpretBuiltin(t, c)
		if !res.Sufficient {
			t.Errorf("%s: want sufficient, got %q", c, res.Insufficiency)
		}
		for _, e := range res.Effects {
			if e.Kind == EffectPath {
				t.Errorf("%s: a test operand must not become a path effect, got %v", c, e)
			}
		}
	}
	for _, c := range []string{`[ -v NAME ]`, `[ -R NAME ]`, `[ -Q x ]`, `test -v NAME`} {
		res := interpretBuiltin(t, c)
		if res.Sufficient || !strings.Contains(res.Insufficiency, "unknown flag") {
			t.Errorf("%s: want insufficient (unknown flag), got sufficient=%v %q", c, res.Sufficient, res.Insufficiency)
		}
	}
}

// TestReadBuiltin: every NAME read assigns is an EffectEnv SET marked as a
// shell-variable write; `-a NAME` names one the same way; `-u` is unmodeled.
func TestReadBuiltin(t *testing.T) {
	res := interpretBuiltin(t, `read -r -p "go? " first second`)
	if !res.Sufficient {
		t.Fatalf("insufficient: %s", res.Insufficiency)
	}
	var names []string
	for _, e := range res.Effects {
		if e.Kind == EffectEnv {
			if !e.EnvSet || !e.EnvPersistent {
				t.Errorf("read's variable write must be a persistent EffectEnv set, got %+v", e)
			}
			names = append(names, e.EnvName)
		}
	}
	if strings.Join(names, ",") != "first,second" {
		t.Errorf("assigned names = %v, want first,second", names)
	}
	res = interpretBuiltin(t, `read -a arr`)
	if !res.Sufficient {
		t.Fatalf("read -a: %s", res.Insufficiency)
	}
	found := false
	for _, e := range res.Effects {
		if e.Kind == EffectEnv && e.EnvName == "arr" {
			found = true
		}
	}
	if !found {
		t.Errorf("read -a arr must assign arr: %+v", res.Effects)
	}
	if res := interpretBuiltin(t, `read -u 3 line`); res.Sufficient {
		t.Errorf("read -u must stay unmodeled")
	}
	if res := interpretBuiltin(t, `read -r "$name"`); res.Sufficient {
		t.Errorf("a runtime variable name must be insufficient")
	}
}

// TestPwdShiftExitBuiltins: pwd's output is metadata (not content); shift and
// exit take a status/count operand and nothing else.
func TestPwdShiftExitBuiltins(t *testing.T) {
	res := interpretBuiltin(t, `pwd -P`)
	if !res.Sufficient {
		t.Fatalf("pwd -P: %s", res.Insufficiency)
	}
	for _, e := range res.Effects {
		if e.Kind == EffectStdio && e.Stream == StreamStdout && !e.Metadata {
			t.Errorf("pwd's stdout is path metadata, not content: %+v", e)
		}
	}
	for _, c := range []string{`shift`, `shift 2`, `exit`, `exit 3`, `exit -1`} {
		if res := interpretBuiltin(t, c); !res.Sufficient || len(res.Effects) != 0 {
			t.Errorf("%s: want sufficient with no effects, got %+v", c, res)
		}
	}
	if res := interpretBuiltin(t, `pwd --bogus`); res.Sufficient {
		t.Errorf("pwd with an unknown flag must be insufficient")
	}
	if res := interpretBuiltin(t, `shift -x`); res.Sufficient {
		t.Errorf("shift with an unknown flag must be insufficient")
	}
}

// env_allowlist_fuzz_test.go - tc-o14i5.3.15 (Phase 2m, design item m).
//
// FuzzEnvAllowlist fuzzes the unified env allowlist (EnvAssignment, packet
// tc-o14i5.3.3: envVarClasses plus persistentShellBehaviorVars) over the full
// input space of an EffectEnv - NAME, VALUE, the value's expansion census, and
// the three leaf-level flags (EnvCleared, EnvGitInvoking, EnvPersistent) - and
// checks the verdict against an INDEPENDENT statement of the allowlist below
// (the table is re-spelled here, not read from the package's own maps, so a
// silently dropped or reclassified name is caught instead of mirrored).
//
// Invariants, for every input:
//
//  1. Judge never panics, and always applies to an EffectEnv.
//  2. A read is always Permitted; a Dynamic NAME is always Unknown.
//  3. An injector name (LD_PRELOAD, DYLD_INSERT_LIBRARIES, LD_LIBRARY_PATH,
//     DYLD_LIBRARY_PATH, BASH_ENV, ZDOTDIR) set statically is Forbidden whatever
//     the VALUE or flags; ENV is never Permitted.
//  4. GIT_DIR / GIT_INDEX_FILE: Forbidden iff the leaf invokes git or the
//     assignment is persistent, else Permitted (tc-j0aa narrowing).
//  5. A PERSISTENT assignment of a shell-behaviour variable (IFS, CDPATH, ...)
//     is never Permitted.
//  6. PATH / HOME are never Permitted on a value that is not made only of
//     static absolute components (plus the caller's own $NAME / ${NAME} and
//     HOME's mktemp -d idiom), and a PATH value naming a directory under the
//     agent-writable /tmp zone is never Permitted.
//  7. Any other static name is Permitted unless invariant 5 applies.
//
// Run as a plain test it replays the seed corpus (checked in under
// testdata/fuzz/FuzzEnvAllowlist plus the f.Add seeds below); run as a fuzzer
// with `go test -run '^$' -fuzz FuzzEnvAllowlist -fuzztime 60s
// ./internal/effectpolicy/`.
package effectpolicy

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// Independent copy of the allowlist under test (see the file comment).
var (
	fuzzInjectors = map[string]bool{
		"LD_PRELOAD": true, "DYLD_INSERT_LIBRARIES": true, "LD_LIBRARY_PATH": true,
		"DYLD_LIBRARY_PATH": true, "BASH_ENV": true, "ZDOTDIR": true,
	}
	fuzzShellBehaviour = map[string]bool{
		"IFS": true, "CDPATH": true, "GLOBIGNORE": true, "PWD": true, "OLDPWD": true,
		"PS4": true, "PROMPT_COMMAND": true, "SHELLOPTS": true, "BASHOPTS": true,
		"BASH_XTRACEFD": true, "TMPDIR": true,
	}
)

func FuzzEnvAllowlist(f *testing.F) {
	type seed struct {
		name, value                string
		expansion                  uint8
		set, dynamic               bool
		cleared, gitInvoking, pers bool
	}
	for _, s := range []seed{
		{"LD_PRELOAD", "/tmp/evil.so", 0, true, false, false, false, false},
		{"BASH_ENV", "", 0, true, false, false, false, false},
		{"ENV", "/etc/profile", 0, true, false, false, false, false},
		{"PATH", "/usr/bin:/bin", 0, true, false, true, false, false},
		{"PATH", "/tmp/evil:$PATH", 1, true, false, false, false, false},
		{"PATH", "/usr/local/bin:$PATH", 1, true, false, false, false, false},
		{"PATH", "$(echo /tmp/x):$PATH", 3, true, false, false, false, false},
		{"PATH", ":/usr/bin", 0, true, false, true, false, false},
		{"HOME", "$(mktemp -d)", 3, true, false, false, false, false},
		{"HOME", "/home/someone", 0, true, false, false, false, false},
		{"GIT_DIR", "/tmp/x/.git", 0, true, false, false, true, false},
		{"GIT_DIR", "/tmp/x/.git", 0, true, false, false, false, false},
		{"GIT_INDEX_FILE", "/tmp/idx", 0, true, false, false, false, true},
		{"IFS", "=", 0, true, false, false, false, true},
		{"IFS", "=", 0, true, false, false, false, false},
		{"CDPATH", "/", 0, true, false, false, false, true},
		{"FOO", "bar", 0, true, false, false, false, false},
		{"FOO", "$(rm -rf ~)", 3, true, false, false, false, false},
		{"", "", 0, true, false, false, false, false},
		{"PATH", "", 0, false, false, false, false, false},
		{"$X", "v", 0, true, true, false, false, false},
		{"ld_preload", "/tmp/evil.so", 0, true, false, false, false, false},
	} {
		f.Add(s.name, s.value, s.expansion, s.set, s.dynamic, s.cleared, s.gitInvoking, s.pers)
	}

	root := f.TempDir()
	pctx := PolicyContext{PathEval: patheval.NewWithCWD(root, root), CWD: root}

	f.Fuzz(func(t *testing.T, name, value string, expansion uint8, set, dynamic, cleared, gitInvoking, persistent bool) {
		// A NUL byte cannot occur in a real env name/value (C strings; the shell
		// parser drops it from a word), so inputs carrying one are outside the
		// policy's domain: skipped, not a finding.
		if strings.ContainsRune(name, 0) || strings.ContainsRune(value, 0) {
			t.Skip("NUL byte cannot occur in a shell-sourced env assignment")
		}
		e := cmddesc.Effect{
			Kind:           cmddesc.EffectEnv,
			EnvName:        name,
			EnvSet:         set,
			EnvValue:       value,
			EnvExpansion:   cmdparse.ExpansionKind(int(expansion) % 8),
			EnvCleared:     cleared,
			EnvGitInvoking: gitInvoking,
			EnvPersistent:  persistent,
			Dynamic:        dynamic,
		}
		fnd, applies := EnvAssignment{}.Judge(e, pctx)
		if !applies {
			t.Fatalf("EnvAssignment does not apply to an EffectEnv: %+v", e)
		}
		v := fnd.Verdict

		switch {
		case !set:
			if v != Permitted {
				t.Fatalf("read of %q judged %v, want Permitted", name, v)
			}
			return
		case dynamic:
			if v != Unknown {
				t.Fatalf("dynamic NAME judged %v, want Unknown", v)
			}
			return
		}

		behaviour := persistent && fuzzShellBehaviour[name]
		switch {
		case behaviour:
			if v == Permitted {
				t.Fatalf("persistent shell-behaviour %q judged Permitted", name)
			}
		case fuzzInjectors[name]:
			if v != Forbidden {
				t.Fatalf("injector %q=%q judged %v, want Forbidden", name, value, v)
			}
		case name == "ENV":
			if v == Permitted {
				t.Fatalf("ENV=%q judged Permitted", value)
			}
		case name == "GIT_DIR" || name == "GIT_INDEX_FILE":
			want := Permitted
			if gitInvoking || persistent {
				want = Forbidden
			}
			if v != want {
				t.Fatalf("%s (gitInvoking=%v persistent=%v) judged %v, want %v", name, gitInvoking, persistent, v, want)
			}
		case name == "PATH" || name == "HOME":
			if v == Permitted && !fuzzPermittedValueShape(name, value) {
				t.Fatalf("%s=%q judged Permitted but is not made only of static absolute components / $%s / mktemp -d", name, value, name)
			}
			if v == Permitted && name == "PATH" {
				for _, c := range strings.Split(value, ":") {
					if c == "/tmp" || strings.HasPrefix(c, "/tmp/") {
						t.Fatalf("PATH=%q judged Permitted with agent-writable component %q", value, c)
					}
				}
			}
		default:
			if v != Permitted {
				t.Fatalf("unclassified %q=%q judged %v, want Permitted", name, value, v)
			}
		}
	})
}

// fuzzPermittedValueShape is the independent oracle for a Permitted PATH/HOME
// value: every ':'-component is a static absolute path (no expansion/quote/
// control bytes) or the caller's own $NAME / ${NAME}; HOME alone may instead be
// the fresh-temp-dir idiom. It deliberately does not call the package's own
// envIsStaticAbsolutePath.
func fuzzPermittedValueShape(name, value string) bool {
	if name == "HOME" && (strings.HasPrefix(value, "$(mktemp") || strings.HasPrefix(value, "`mktemp")) {
		return true
	}
	for _, c := range strings.Split(value, ":") {
		if c == "$"+name || c == "${"+name+"}" {
			continue
		}
		if !strings.HasPrefix(c, "/") {
			return false
		}
		for i := 0; i < len(c); i++ {
			b := c[i]
			if b == '$' || b == '`' || b == '"' || b == '\'' || b == '\\' || b < 0x20 || b == 0x7f {
				return false
			}
		}
	}
	return true
}

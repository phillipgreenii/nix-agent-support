package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-wrap/internal/spec"
)

// recorder stands in for the process-image replacement so a test can read what
// would have been executed. The real exec is covered by the e2e tests.
type recorder struct {
	called bool
	path   string
	argv   []string
	env    []string
	err    error
}

func (r *recorder) exec(path string, argv, env []string) error {
	r.called, r.path, r.argv, r.env = true, path, argv, env
	return r.err
}

func writeSpec(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spec.toml")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func process(r *recorder, environ []string, args ...string) (Process, *bytes.Buffer, *bytes.Buffer) {
	var so, se bytes.Buffer
	return Process{Args: args, Environ: environ, Stdout: &so, Stderr: &se, Exec: r.exec}, &so, &se
}

func TestRunExecsTheComposedChain(t *testing.T) {
	spec := writeSpec(t, "schema_version = 1\nwrap = [\"/usr/bin/env A=1\", [\"/usr/bin/env\", \"B=2\"]]\nexec = [\"/opt/real\"]\n")
	r := &recorder{}
	p, so, se := process(r, []string{"X=1", "PG_WRAP_SPEC=/s"}, "run", "--spec", spec, "--", "arg one", "--")
	if code := Main(p); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, se)
	}
	if !r.called || r.path != "/bin/sh" {
		t.Fatalf("exec = %+v", r)
	}
	want := []string{"/bin/sh", "-c", `exec /usr/bin/env A=1 "$@"`, "sh", "/usr/bin/env", "B=2", "/opt/real", "arg one", "--"}
	if !reflect.DeepEqual(r.argv, want) {
		t.Fatalf("argv = %q\nwant %q", r.argv, want)
	}
	if !reflect.DeepEqual(r.env, []string{"X=1"}) {
		t.Fatalf("env = %q: the inherited env minus PG_WRAP_SPEC", r.env)
	}
	if so.Len() != 0 || se.Len() != 0 {
		t.Fatalf("pg-wrap wrote output on a clean run: %q %q", so, se)
	}
}

func TestExecFailureIsAnExecStageFailure(t *testing.T) {
	spec := writeSpec(t, "schema_version = 1\nwrap = [[\"/no/such\"]]\n")
	r := &recorder{err: errors.New("no such file or directory")}
	p, _, se := process(r, nil, "run", "--spec", spec, "--", "x")
	if code := Main(p); code != 97 {
		t.Fatalf("code=%d", code)
	}
	if want := "pg-wrap[exec]: cannot exec /no/such: no such file or directory\n"; se.String() != want {
		t.Fatalf("stderr = %q, want %q", se, want)
	}
}

func TestRelativeFirstWordNamesItsLayerAndNeverExecs(t *testing.T) {
	spec := writeSpec(t, "schema_version = 1\nwrap = [\"/usr/bin/env A=1\", \"env B=2\"]\n")
	r := &recorder{}
	p, _, se := process(r, nil, "run", "--spec", spec, "--", "x")
	if code := Main(p); code != 97 || r.called {
		t.Fatalf("code=%d called=%v", code, r.called)
	}
	if !strings.HasPrefix(se.String(), "pg-wrap[layer 1]: wrap[1]: first word \"env\" of a string entry must be an absolute path") {
		t.Fatalf("stderr = %q", se)
	}
}

func TestExecMainTakesTheSpecFromTheEnvironmentAndAllArgsArePayload(t *testing.T) {
	spec := writeSpec(t, "schema_version = 1\nexec = [\"/opt/real\"]\n")
	r := &recorder{}
	p, _, se := process(r, []string{"PG_WRAP_SPEC=" + spec, "K=v"}, "run", "check", "", "--spec")
	if code := ExecMain(p); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, se)
	}
	if r.path != "/opt/real" || !reflect.DeepEqual(r.argv, []string{"/opt/real", "run", "check", "", "--spec"}) {
		t.Fatalf("exec = %+v", r)
	}
	if !reflect.DeepEqual(r.env, []string{"K=v"}) {
		t.Fatalf("env = %q", r.env)
	}
}

func TestExecMainWithoutASpecNeverExecs(t *testing.T) {
	r := &recorder{}
	p, so, se := process(r, []string{"K=v"}, "/opt/real")
	if code := ExecMain(p); code != 97 || r.called || so.Len() != 0 {
		t.Fatalf("code=%d called=%v stdout=%q", code, r.called, so)
	}
	if want := "pg-wrap[spec]: " + NoSpecReason + "\n"; se.String() != want {
		t.Fatalf("stderr = %q", se)
	}
}

func TestBuildEnv(t *testing.T) {
	environ := []string{"HOME=/h", "SECRET=x", "PG_WRAP_SPEC=/s", "KEEPME=1", "PG_WRAP_SPEC_OTHER=2", "malformed"}
	t.Run("inherited", func(t *testing.T) {
		got, err := buildEnv(&spec.Spec{}, environ)
		want := []string{"HOME=/h", "SECRET=x", "KEEPME=1", "PG_WRAP_SPEC_OTHER=2", "malformed"}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q %v, want %q", got, err, want)
		}
	})
	t.Run("minimal plus keep, spec variable removed even when kept", func(t *testing.T) {
		got, err := buildEnv(&spec.Spec{StrictEnv: true, Keep: []string{"KEEPME", "PG_WRAP_SPEC"}}, environ)
		want := []string{"HOME=/h", "KEEPME=1", "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q %v, want %q", got, err, want)
		}
	})
	t.Run("a keep name that cannot be a variable name", func(t *testing.T) {
		for _, bad := range []string{"", "A=B", "A\x00"} {
			if _, err := buildEnv(&spec.Spec{StrictEnv: true, Keep: []string{bad}}, environ); err == nil ||
				!strings.HasPrefix(err.Error(), "pg-wrap[env]: ") {
				t.Errorf("keep %q: err = %v", bad, err)
			}
		}
	})
}

func TestLookPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	plain := filepath.Join(dir, "data")
	for p, mode := range map[string]os.FileMode{exe: 0o755, plain: 0o644} {
		if err := os.WriteFile(p, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=/nonexistent:" + dir}
	if got, err := lookPath("tool", env); err != nil || got != exe {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := lookPath("data", env); err == nil {
		t.Fatal("a non-executable file must not resolve")
	}
	if _, err := lookPath("absent", env); err == nil || !strings.Contains(err.Error(), "not found in PATH") {
		t.Fatalf("err = %v", err)
	}
	if got, err := lookPath("./rel/tool", env); err != nil || got != "./rel/tool" {
		t.Fatalf("a name with a slash is used as given: %q %v", got, err)
	}
}

func TestLookupEnvLastWins(t *testing.T) {
	if got := lookupEnv([]string{"A=1", "A=2", "B=3"}, "A"); got != "2" {
		t.Fatalf("got %q", got)
	}
	if got := lookupEnv([]string{"AB=1"}, "A"); got != "" {
		t.Fatalf("a prefix must not match: %q", got)
	}
}

func TestCheckAndShowRejectStrayArguments(t *testing.T) {
	spec := writeSpec(t, "schema_version = 1\n")
	for _, verb := range []string{"check", "show"} {
		r := &recorder{}
		p, _, se := process(r, nil, verb, "--spec", spec, "extra")
		if code := Main(p); code != 97 || !strings.Contains(se.String(), `unexpected argument "extra"`) {
			t.Fatalf("%s: code=%d stderr=%q", verb, code, se)
		}
	}
}

func TestEqualsFormOfTheSpecFlag(t *testing.T) {
	spec := writeSpec(t, "schema_version = 1\n")
	r := &recorder{}
	p, _, se := process(r, nil, "run", "--spec="+spec, "--", "/opt/x", "a")
	if code := Main(p); code != 0 || r.path != "/opt/x" {
		t.Fatalf("code=%d path=%q stderr=%q", code, r.path, se)
	}
}

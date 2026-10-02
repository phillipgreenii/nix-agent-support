package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeEnvFile(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "h.env")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildEnvPrecedence(t *testing.T) {
	envf := writeEnvFile(t, "B=file\nC=file\nD=file\n", 0o600)
	h := &Handler{
		Name:    "h",
		Env:     map[string]string{"A": "env", "B": "env", "C": "env"},
		EnvFile: envf,
	}
	base := []string{"A=wrapper", "Z=wrapper", "B=wrapper", "C=wrapper", "D=wrapper", "PG_RESCUE_RUN_ID=stale"}
	got, err := BuildEnv(base, h, map[string]string{"C": "pg", "PG_RESCUE_RUN_ID": "real"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"A=env",  // env beats the wrapper's environment
		"B=file", // env_file beats env
		"C=pg",   // PG_RESCUE_* (wrapper variables) beat env_file
		"D=file", // env_file beats the wrapper's environment
		"PG_RESCUE_RUN_ID=real",
		"Z=wrapper", // untouched wrapper variable survives
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestBuildEnvWithoutOptionalSources(t *testing.T) {
	got, err := BuildEnv([]string{"A=1", "malformed"}, &Handler{Name: "h"}, nil)
	if err != nil || !reflect.DeepEqual(got, []string{"A=1"}) {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestBuildEnvValuesMayContainEquals(t *testing.T) {
	got, _ := BuildEnv([]string{"URL=a=b=c"}, &Handler{Name: "h"}, nil)
	if !reflect.DeepEqual(got, []string{"URL=a=b=c"}) {
		t.Errorf("got %v", got)
	}
}

func TestBuildEnvReadsEnvFileAtSpawnTime(t *testing.T) {
	h := &Handler{Name: "h", EnvFile: filepath.Join(t.TempDir(), "absent.env")}
	_, err := BuildEnv(nil, h, nil)
	if err == nil || !strings.Contains(err.Error(), `[handler.h] key "env_file"`) {
		t.Errorf("missing env_file at spawn: %v", err)
	}
}

func TestReadEnvFileRefusesLoosenedPermissions(t *testing.T) {
	p := writeEnvFile(t, "A=1\n", 0o644)
	if _, err := ReadEnvFile(p); err == nil || !strings.Contains(err.Error(), "unsafe permissions") {
		t.Errorf("err = %v", err)
	}
}

func TestReadEnvFileSyntax(t *testing.T) {
	body := `# a comment

PLAIN=value
export EXPORTED=yes
SPACED = padded value
EMPTY=
SINGLE='no $interp \n'
DOUBLE="tab\there\nnewline \"q\" back\\slash"
TRAIL=value # inline comment
HASH=a#b
`
	got, err := ReadEnvFile(writeEnvFile(t, body, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"PLAIN": "value", "EXPORTED": "yes", "SPACED": "padded value", "EMPTY": "",
		"SINGLE": `no $interp \n`, "DOUBLE": "tab\there\nnewline \"q\" back\\slash",
		"TRAIL": "value", "HASH": "a#b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %#v\nwant %#v", got, want)
	}
}

func TestReadEnvFileErrorsNameFileAndLine(t *testing.T) {
	for name, body := range map[string]string{
		"no equals":     "A=1\nnot a pair\n",
		"empty key":     "=v\n",
		"key has space": "A B=v\n",
		"open single":   "A='x\n",
		"open double":   "A=\"x\n",
	} {
		p := writeEnvFile(t, body, 0o600)
		_, err := ReadEnvFile(p)
		if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "line ") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

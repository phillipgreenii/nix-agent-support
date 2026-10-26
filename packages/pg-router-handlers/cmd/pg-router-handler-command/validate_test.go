package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
)

func validate(t *testing.T, req string) (int, map[string]any) {
	t.Helper()
	var out bytes.Buffer
	exit := serveValidate(strings.NewReader(req), &out)
	return exit, decodeReply(t, out.String())
}

func problemsOf(t *testing.T, m map[string]any) []string {
	t.Helper()
	raw, ok := m["problems"].([]any)
	if !ok {
		t.Fatalf("reply has no problems array: %v", m)
	}
	out := make([]string, len(raw))
	for i, p := range raw {
		out[i], _ = p.(string)
	}
	return out
}

func TestValidate_ValidConfigHasNoProblems(t *testing.T) {
	exit, m := validate(t, request(t, nil, "cmdrole", cmdConfig(true, "/usr/bin/true", "{{.BeadID}}"), true))
	if exit != conformance.ExitOK {
		t.Fatalf("exit = %d", exit)
	}
	if err := conformance.Check("handler.validate-reply", m); err != nil {
		t.Fatalf("reply is not conformant: %v", err)
	}
	if p := problemsOf(t, m); len(p) != 0 {
		t.Fatalf("problems = %v, want none", p)
	}
}

// TestValidate_ReportsEveryProblemInOneReply: several independent defects are all
// in the one reply, exit 0, in a stable order (shape first, keys sorted).
func TestValidate_ReportsEveryProblemInOneReply(t *testing.T) {
	cfg := map[string]any{
		"zeta":          1,
		"alpha":         true,
		"strictBaseEnv": "yes",
		"command":       map[string]any{"argv": []any{"/usr/bin/true", 7}, "cwd": "/"},
	}
	exit, m := validate(t, request(t, nil, "cmdrole", cfg, true))
	if exit != conformance.ExitOK {
		t.Fatalf("a validate that finds problems must still exit 0, got %d", exit)
	}
	got := problemsOf(t, m)
	// The walk visits keys in sorted order (alpha, command, strictBaseEnv,
	// zeta), and inside command argv before cwd.
	want := []string{
		`role.config: unknown key "alpha"`,
		"role.config.command.argv[1]: want a string, got a number",
		`role.config.command: unknown key "cwd"`,
		"role.config.strictBaseEnv: want a boolean, got a string",
		`role.config: unknown key "zeta"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("problems =\n%v\nwant\n%v", got, want)
	}
}

// TestValidate_LoadRulesAreReportedTogether: the rules dispatch applies after the
// shape (command and argv required, the program non-empty, each element a
// parseable template) are problems too, every offending element named.
func TestValidate_LoadRulesAreReportedTogether(t *testing.T) {
	cases := map[string]struct {
		cfg  map[string]any
		want []string
	}{
		"no command":  {map[string]any{}, []string{"role.config.command: required"}},
		"empty argv":  {map[string]any{"command": map[string]any{"argv": []any{}}}, []string{"role.config.command.argv: must hold at least one element"}},
		"empty prog":  {cmdConfig(false, ""), []string{"role.config.command.argv[0]: the program to run is empty"}},
		"two bad tpl": {cmdConfig(false, "/bin/echo", "{{.BeadID", "{{end}}"), []string{"role.config.command.argv[1]: template:", "role.config.command.argv[2]: template:"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			exit, m := validate(t, request(t, nil, "cmdrole", c.cfg, true))
			if exit != conformance.ExitOK {
				t.Fatalf("exit = %d", exit)
			}
			got := problemsOf(t, m)
			if len(got) != len(c.want) {
				t.Fatalf("problems = %v, want %d of them (%v)", got, len(c.want), c.want)
			}
			for i, w := range c.want {
				if !strings.HasPrefix(got[i], w) {
					t.Errorf("problem %d = %q, want prefix %q", i, got[i], w)
				}
			}
		})
	}
}

func TestValidate_MissingConfigIsEmptyConfig(t *testing.T) {
	_, m := validate(t, request(t, nil, "cmdrole", nil, true))
	if got := problemsOf(t, m); !reflect.DeepEqual(got, []string{"role.config.command: required (a role of this adapter runs command.argv)"}) {
		t.Fatalf("problems = %v", got)
	}
}

func TestValidate_RequestWithoutRoleIsUsageError(t *testing.T) {
	var out bytes.Buffer
	exit := serveValidate(strings.NewReader(request(t, nil, "", nil, false)), &out)
	if exit != conformance.ExitUsage {
		t.Fatalf("exit = %d, want %d; %s", exit, conformance.ExitUsage, out.String())
	}
	if _, ok := decodeReply(t, out.String())["error"].(string); !ok {
		t.Fatalf("want an error reply, got %s", out.String())
	}
}

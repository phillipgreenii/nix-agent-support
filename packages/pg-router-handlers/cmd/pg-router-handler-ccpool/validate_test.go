package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

type validateReplyBody struct {
	SchemaVersion string   `json:"schemaVersion"`
	Problems      []string `json:"problems"`
	Warnings      []string `json:"warnings"`
}

// validateWith drives serveValidate with the role, asserts the reply is
// schema-valid and exit 0, and returns the decoded reply.
func validateWith(t *testing.T, name string, cfg map[string]any, skipPoolDirs bool) validateReplyBody {
	t.Helper()
	var out bytes.Buffer
	if code := serveValidate(strings.NewReader(request("", name, cfg)), &out, skipPoolDirs); code != conformance.ExitOK {
		t.Fatalf("exit = %d, want %d; stdout %s", code, conformance.ExitOK, out.String())
	}
	if err := schemas.ValidateBytes("handler.validate-reply", out.Bytes()); err != nil {
		t.Fatalf("reply %s does not match handler.validate-reply: %v", out.String(), err)
	}
	var r validateReplyBody
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// ccpoolConfig renders a [role.config] shaped like the nix module's ccpool
// rendering. extra is spliced into the top-level object, ccpoolExtra into the
// ccpool table.
func ccpoolConfig(t *testing.T, poolDir, extra, ccpoolExtra string) map[string]any {
	t.Helper()
	return configFrom(t, fmt.Sprintf(`{%s"ccpool":{"actor":"r","skillMD":"","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim",
"promptBody":"p","isolation":{"Type":"none"},"poolDir":%q%s}}`, extra, poolDir, ccpoolExtra))
}

func requireOneContaining(t *testing.T, what string, got []string, frag string) {
	t.Helper()
	if len(got) != 1 || !strings.Contains(got[0], frag) {
		t.Fatalf("%s = %q, want exactly one containing %q", what, got, frag)
	}
}

func TestValidate_nixShapedRoleIsValid(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	cfg := ccpoolConfig(t, t.TempDir(), `"sessionEnv":{"PG_WRAP_SPEC":"/nix/store/x-spec.toml","LANG":"C"},`, `,"authorshipGuard":false,"budget":{"tokens":0,"cost":0,"time":"25m"}`)
	r := validateWith(t, "r", cfg, false)
	if len(r.Problems) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("problems=%q warnings=%q, want none (case-insensitive Isolation keys and sessionEnv are valid)", r.Problems, r.Warnings)
	}
}

func TestValidate_strictShape(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	cases := []struct {
		name string
		cfg  map[string]any
		want string
	}{
		{"unknown top-level key", ccpoolConfig(t, pool, `"surviveShutdown":true,`, ""), `unknown key "surviveShutdown"`},
		{"survivesShutdown is core's key, not config", ccpoolConfig(t, pool, `"survivesShutdown":true,`, ""), `unknown key "survivesShutdown"`},
		{"name is the request's role.name", ccpoolConfig(t, pool, `"name":"r",`, ""), `unknown key "name"`},
		{"type is not config", ccpoolConfig(t, pool, `"type":"ccpool",`, ""), `unknown key "type"`},
		{"strictBaseEnv is a command-role key", ccpoolConfig(t, pool, `"strictBaseEnv":true,`, ""), `unknown key "strictBaseEnv"`},
		{"command is the other adapter's key", ccpoolConfig(t, pool, `"command":{"argv":["x"]},`, ""), `unknown key "command"`},
		{"unknown key inside ccpool", ccpoolConfig(t, pool, "", `,"actr":"x"`), `ccpool: unknown key "actr"`},
		{"unknown key inside isolation", configFrom(t, strings.Replace(`{"ccpool":{"actor":"r","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"p","isolation":{"Type":"none"}}}`, `{"Type":"none"}`, `{"Type":"none","Bogus":1}`, 1)), `ccpool.isolation: unknown key "Bogus"`},
		{"wrong type inside ccpool", ccpoolConfig(t, pool, "", `,"extraAllowedTools":"Bash"`), "ccpool.extraAllowedTools: want an array, got a string"},
		{"non-string sessionEnv value", ccpoolConfig(t, pool, `"sessionEnv":{"A":1},`, ""), "sessionEnv.A: want a string, got a number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireOneContaining(t, "problems", validateWith(t, "r", tc.cfg, false).Problems, tc.want)
		})
	}
}

// validate is the one entry point and reports EVERY problem in one reply, in a
// stable order: unknown keys sorted, then the shape, then the load rules.
func TestValidate_reportsEveryProblemInOneReplyInStableOrder(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	cfg := ccpoolConfig(t, pool, `"zzz":1,"aaa":2,`, `,"budget":{"tokens":"many"}`)
	a := validateWith(t, "r", cfg, false)
	b := validateWith(t, "r", cfg, false)
	if len(a.Problems) != 3 {
		t.Fatalf("problems = %q, want 3", a.Problems)
	}
	if strings.Join(a.Problems, "\n") != strings.Join(b.Problems, "\n") {
		t.Fatalf("order not stable: %q vs %q", a.Problems, b.Problems)
	}
	if !strings.Contains(a.Problems[0], `"aaa"`) || !strings.Contains(a.Problems[1], `"zzz"`) {
		t.Errorf("unknown keys not sorted: %q", a.Problems)
	}

	// Problems of different kinds: a bad sessionEnv key, a bad load rule and a
	// missing pool directory arrive together.
	missing := filepath.Join(t.TempDir(), "no-such-pool")
	mixed := ccpoolConfig(t, missing, `"sessionEnv":{"BEADS_DIR":"x"},`, `,"effort":"ludicrous","budget":{"time":"soon"}`)
	mixed["ccpool"].(map[string]any)["actor"] = ""
	r := validateWith(t, "r", mixed, false)
	if len(r.Problems) < 3 {
		t.Fatalf("problems = %q, want the sessionEnv, load-rule and pool problems together", r.Problems)
	}
	joined := strings.Join(r.Problems, "\n")
	for _, frag := range []string{"sessionEnv", "ccpool.actor is required", missing} {
		if !strings.Contains(joined, frag) {
			t.Errorf("problems lack %q:\n%s", frag, joined)
		}
	}
}

func TestValidate_sessionEnvLoadRules(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	for _, key := range []string{"BEADS_ACTOR", "BEADS_DIR", "WORKSPACE_ROOT", "PG_ROUTER_ROLE", "", "A=B"} {
		t.Run(key, func(t *testing.T) {
			env, _ := json.Marshal(map[string]string{key: "v"})
			r := validateWith(t, "r", ccpoolConfig(t, pool, `"sessionEnv":`+string(env)+",", ""), false)
			requireOneContaining(t, "problems", r.Problems, "sessionEnv: ")
		})
	}
	t.Run("every offending key is reported", func(t *testing.T) {
		r := validateWith(t, "r", ccpoolConfig(t, pool, `"sessionEnv":{"BEADS_DIR":"x","PG_ROUTER_X":"y","OK":"z"},`, ""), false)
		if len(r.Problems) != 2 {
			t.Fatalf("problems = %q, want one per offending key", r.Problems)
		}
	})
}

func TestValidate_secretLookingKeyWarns(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	for _, key := range []string{"GH_TOKEN", "MY_SECRET", "API_KEY", "DB_PASSWORD", "gh_token"} {
		t.Run(key, func(t *testing.T) {
			r := validateWith(t, "r", ccpoolConfig(t, pool, fmt.Sprintf(`"sessionEnv":{%q:"x"},`, key), ""), false)
			if len(r.Problems) != 0 {
				t.Fatalf("problems = %q, want none: a secret-looking key is a warning only", r.Problems)
			}
			requireOneContaining(t, "warnings", r.Warnings, key)
		})
	}
}

func TestValidate_loadRulesAreProblems(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	withActor := func(actor string) map[string]any {
		c := ccpoolConfig(t, pool, "", "")
		c["ccpool"].(map[string]any)["actor"] = actor
		return c
	}
	withCompletion := func(v string) map[string]any {
		c := ccpoolConfig(t, pool, "", "")
		c["ccpool"].(map[string]any)["completion"] = v
		return c
	}
	cases := []struct {
		name string
		cfg  map[string]any
		want string
	}{
		{"empty actor", withActor(""), "ccpool.actor is required"},
		{"bad enum", withCompletion("nope"), "invalid completion"},
		{"bad budget time", ccpoolConfig(t, pool, "", `,"budget":{"time":"soon"}`), "budget.time"},
		{"bad effort", ccpoolConfig(t, pool, "", `,"effort":"ludicrous"`), "ludicrous"},
		{"bad model", ccpoolConfig(t, pool, "", `,"model":"-p"`), `model "-p"`},
		{"ccpool table missing", map[string]any{}, "role.config.ccpool: required"},
		{"unparseable template", ccpoolConfig(t, pool, "", `,"promptBody":"{{"`), "promptBody"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireOneContaining(t, "problems", validateWith(t, "r", tc.cfg, false).Problems, tc.want)
		})
	}
}

func TestValidate_poolDirectory(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	missing := filepath.Join(t.TempDir(), "no-such-pool")
	noPoolDir := func() map[string]any {
		c := ccpoolConfig(t, "", "", "")
		delete(c["ccpool"].(map[string]any), "poolDir")
		return c
	}

	t.Run("missing poolDir is a problem", func(t *testing.T) {
		requireOneContaining(t, "problems", validateWith(t, "r", ccpoolConfig(t, missing, "", ""), false).Problems, missing)
	})
	t.Run("--skip-pool-dirs: neither problem nor warning", func(t *testing.T) {
		r := validateWith(t, "r", ccpoolConfig(t, missing, "", ""), true)
		if len(r.Problems) != 0 || len(r.Warnings) != 0 {
			t.Fatalf("problems=%q warnings=%q, want none", r.Problems, r.Warnings)
		}
	})
	t.Run("--skip-pool-dirs skips only the pool check", func(t *testing.T) {
		r := validateWith(t, "r", ccpoolConfig(t, missing, `"nope":1,`, ""), true)
		requireOneContaining(t, "problems", r.Problems, `unknown key "nope"`)
	})
	t.Run("a file is not a pool directory", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		requireOneContaining(t, "problems", validateWith(t, "r", ccpoolConfig(t, f, "", ""), false).Problems, "not a directory")
	})
	t.Run("CCPOOL_POOL applies when poolDir is unset", func(t *testing.T) {
		t.Setenv("CCPOOL_POOL", missing)
		requireOneContaining(t, "problems", validateWith(t, "r", noPoolDir(), false).Problems, "CCPOOL_POOL")
		t.Setenv("CCPOOL_POOL", t.TempDir())
		if r := validateWith(t, "r", noPoolDir(), false); len(r.Problems) != 0 {
			t.Fatalf("an existing CCPOOL_POOL: problems = %q", r.Problems)
		}
	})
	t.Run("poolDir wins over CCPOOL_POOL", func(t *testing.T) {
		t.Setenv("CCPOOL_POOL", t.TempDir())
		requireOneContaining(t, "problems", validateWith(t, "r", ccpoolConfig(t, missing, "", ""), false).Problems, "ccpool.poolDir")
	})
	t.Run("no pool configured anywhere: nothing to check", func(t *testing.T) {
		if r := validateWith(t, "r", noPoolDir(), false); len(r.Problems) != 0 {
			t.Fatalf("problems = %q", r.Problems)
		}
	})
}

func TestValidate_neverReadsAWrapSpec(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	spec := filepath.Join(t.TempDir(), "does-not-exist.toml")
	r := validateWith(t, "r", ccpoolConfig(t, t.TempDir(), fmt.Sprintf(`"sessionEnv":{"PG_WRAP_SPEC":%q},`, spec), ""), false)
	if len(r.Problems) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("problems=%q warnings=%q: a missing PG_WRAP_SPEC file must not matter to validate", r.Problems, r.Warnings)
	}
}

// A request without a usable role object is the one error validate answers with
// a non-zero exit (usage), still with the error reply the schema allows.
func TestValidate_requestWithoutUsableRoleIsUsage(t *testing.T) {
	for name, body := range map[string]string{
		"no role":    `{"schemaVersion":"1","id":"v-1"}`,
		"empty name": `{"schemaVersion":"1","id":"v-1","role":{"name":"","config":{}}}`,
	} {
		var out bytes.Buffer
		if code := serveValidate(strings.NewReader(body), &out, false); code != conformance.ExitUsage {
			t.Errorf("%s: exit = %d, want %d", name, code, conformance.ExitUsage)
		}
		if _, ok := decodeReply(t, out.String())["error"]; !ok {
			t.Errorf("%s: reply %s has no error", name, out.String())
		}
	}
	// A config that is not an object, or a role that is not an object at all (the
	// schema refuses it), is a malformed request, not a usage error.
	for name, body := range map[string]string{
		"array config":  `{"schemaVersion":"1","id":"v-1","role":{"name":"r","config":[1]}}`,
		"role a string": `{"schemaVersion":"1","id":"v-1","role":"x"}`,
	} {
		var out bytes.Buffer
		if code := serveValidate(strings.NewReader(body), &out, false); code != conformance.ExitError {
			t.Errorf("%s: exit = %d, want %d", name, code, conformance.ExitError)
		}
	}
}

func TestValidate_malformedStdinRequestIsAnErrorReply(t *testing.T) {
	for _, in := range []string{`{not json`, `{"schemaVersion":"1"}`} { // malformed JSON; schema-invalid (no id)
		var out bytes.Buffer
		if code := serveValidate(strings.NewReader(in), &out, false); code != conformance.ExitError {
			t.Fatalf("%q: exit = %d, want %d", in, code, conformance.ExitError)
		}
		if _, ok := decodeReply(t, out.String())["error"]; !ok {
			t.Errorf("%q: reply %s has no error", in, out.String())
		}
	}
}

// runAdapterProcess runs the test binary as the adapter (adapterRunEnv) with
// args and a request on stdin, and returns the exit code and stdout.
func runAdapterProcess(t *testing.T, args []string, stdin string, env ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), append([]string{adapterRunEnv + "=1"}, env...)...)
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), out.String()
	} else if err != nil {
		t.Fatal(err)
	}
	return 0, out.String()
}

// The CLI surface through the real process: --skip-pool-dirs omits only the pool
// check, an unknown flag is a usage error, and the help documents the flag.
func TestProcess_validateFlags(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-pool")
	req := request("", "r", ccpoolConfig(t, missing, "", ""))

	code, out := runAdapterProcess(t, []string{"validate"}, req)
	var r validateReplyBody
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != 0 || len(r.Problems) != 1 {
		t.Fatalf("without the flag: exit=%d problems=%q err=%v, want 0 and the missing-pool problem", code, r.Problems, err)
	}
	code, out = runAdapterProcess(t, []string{"validate", "--skip-pool-dirs"}, req)
	r = validateReplyBody{}
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != 0 || len(r.Problems) != 0 {
		t.Fatalf("with --skip-pool-dirs: exit=%d problems=%q err=%v, want 0 and none", code, r.Problems, err)
	}
	if code, _ = runAdapterProcess(t, []string{"validate", "--bogus"}, req); code != conformance.ExitUsage {
		t.Fatalf("unknown flag: exit = %d, want %d", code, conformance.ExitUsage)
	}
	if !strings.Contains(helpText, "--skip-pool-dirs") || !strings.Contains(usageLine, "validate") {
		t.Errorf("--help must document validate and --skip-pool-dirs")
	}
}

// dispatch and validate agree: a config validate reports clean is a config
// dispatch loads, and one validate rejects is one dispatch refuses before any
// call (the same decoder answers both).
func TestValidateAndDispatchShareOneDecoder(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	for _, key := range []string{"BEADS_DIR", "PG_ROUTER_X"} {
		cfg := ccpoolConfig(t, t.TempDir(), fmt.Sprintf(`"sessionEnv":{%q:"x"},`, key), "")
		if r := validateWith(t, "r", cfg, true); len(r.Problems) != 1 {
			t.Fatalf("validate problems = %q, want one", r.Problems)
		}
		if code, _ := dispatchWith(t, "r", cfg, "", defaultEvent); code != conformance.ExitError {
			t.Errorf("dispatch of a config validate rejects: exit = %d, want %d", code, conformance.ExitError)
		}
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

const validateRequest = `{"schemaVersion":"1","id":"v-1"}`

type validateReplyBody struct {
	SchemaVersion string   `json:"schemaVersion"`
	Problems      []string `json:"problems"`
	Warnings      []string `json:"warnings"`
}

// runServeValidate drives serveValidate with a request and a role-file path,
// asserts the reply is schema-valid, and returns the decoded reply.
func runServeValidate(t *testing.T, rolePath string, skipPoolDirs bool) validateReplyBody {
	t.Helper()
	var out bytes.Buffer
	if code := serveValidate(strings.NewReader(validateRequest), &out, rolePath, skipPoolDirs); code != conformance.ExitOK {
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

func writeRoleFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "role.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// ccpoolRole renders a role file shaped like the nix module's ccpool
// rendering, with top-level survivesShutdown. extra is spliced into the
// top-level object; ccpoolExtra into the ccpool block.
func ccpoolRole(poolDir, extra, ccpoolExtra string) string {
	return fmt.Sprintf(`{"name":"r","type":"ccpool","survivesShutdown":true%s,
"ccpool":{"actor":"r","skillMD":"","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim",
"promptBody":"p","isolation":{"Type":"none"},"poolDir":%q%s}}`, extra, poolDir, ccpoolExtra)
}

func requireOneContaining(t *testing.T, what string, got []string, frag string) {
	t.Helper()
	if len(got) != 1 || !strings.Contains(got[0], frag) {
		t.Fatalf("%s = %q, want exactly one containing %q", what, got, frag)
	}
}

func TestValidate_nixShapedRoleIsValid(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	p := writeRoleFile(t, ccpoolRole(pool, `,"sessionEnv":{"PG_WRAP_SPEC":"/nix/store/x-spec.toml","LANG":"C"}`, `,"authorshipGuard":false,"budget":{"tokens":0,"cost":0,"time":"25m"}`))
	r := runServeValidate(t, p, false)
	if len(r.Problems) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("problems=%q warnings=%q, want none (survivesShutdown, case-insensitive Isolation keys and sessionEnv are valid)", r.Problems, r.Warnings)
	}
}

func TestValidate_commandRoleIsValid(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "/definitely/not/there")
	p := writeRoleFile(t, `{"name":"c","type":"command","strictBaseEnv":true,"command":{"argv":["/bin/echo","hi"]}}`)
	r := runServeValidate(t, p, false)
	if len(r.Problems) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("problems=%q warnings=%q, want none (a command role has no pool)", r.Problems, r.Warnings)
	}
}

func TestValidate_strictShape(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	cases := []struct {
		name, body, want string
	}{
		{"unknown top-level key", ccpoolRole(pool, `,"surviveShutdown":true`, ""), `unknown key "surviveShutdown"`},
		{"wrong type", ccpoolRole(pool, `,"strictBaseEnv":"yes"`, ""), "strictBaseEnv: want a boolean, got a string"},
		{"unknown key inside ccpool", ccpoolRole(pool, "", `,"actr":"x"`), `ccpool: unknown key "actr"`},
		{"unknown key inside isolation", strings.Replace(ccpoolRole(pool, "", ""), `{"Type":"none"}`, `{"Type":"none","Bogus":1}`, 1), `ccpool.isolation: unknown key "Bogus"`},
		{"wrong type inside ccpool", ccpoolRole(pool, "", `,"extraAllowedTools":"Bash"`), "ccpool.extraAllowedTools: want an array, got a string"},
		{"non-string sessionEnv value", ccpoolRole(pool, `,"sessionEnv":{"A":1}`, ""), "sessionEnv.A: want a string, got a number"},
		{"survivesShutdown must be a boolean", strings.Replace(ccpoolRole(pool, "", ""), `"survivesShutdown":true`, `"survivesShutdown":"true"`, 1), "survivesShutdown: want a boolean, got a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runServeValidate(t, writeRoleFile(t, tc.body), false)
			requireOneContaining(t, "problems", r.Problems, tc.want)
		})
	}
}

func TestValidate_reportsEveryShapeProblemInStableOrder(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	p := writeRoleFile(t, ccpoolRole(pool, `,"zzz":1,"aaa":2`, `,"budget":{"tokens":"many"}`))
	a := runServeValidate(t, p, false)
	b := runServeValidate(t, p, false)
	if len(a.Problems) != 3 {
		t.Fatalf("problems = %q, want 3", a.Problems)
	}
	if strings.Join(a.Problems, "\n") != strings.Join(b.Problems, "\n") {
		t.Fatalf("order not stable: %q vs %q", a.Problems, b.Problems)
	}
	if !strings.Contains(a.Problems[0], `"aaa"`) || !strings.Contains(a.Problems[2], `"zzz"`) {
		t.Errorf("unknown keys not sorted: %q", a.Problems)
	}
}

func TestValidate_sessionEnvLoadRules(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	for _, key := range []string{"BEADS_ACTOR", "BEADS_DIR", "WORKSPACE_ROOT", "PG_ROUTER_ROLE", "", "A=B"} {
		t.Run(key, func(t *testing.T) {
			env, _ := json.Marshal(map[string]string{key: "v"})
			r := runServeValidate(t, writeRoleFile(t, ccpoolRole(pool, `,"sessionEnv":`+string(env), "")), false)
			requireOneContaining(t, "problems", r.Problems, "sessionEnv: ")
		})
	}
	t.Run("every offending key is reported", func(t *testing.T) {
		r := runServeValidate(t, writeRoleFile(t, ccpoolRole(pool, `,"sessionEnv":{"BEADS_DIR":"x","PG_ROUTER_X":"y","OK":"z"}`, "")), false)
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
			r := runServeValidate(t, writeRoleFile(t, ccpoolRole(pool, fmt.Sprintf(`,"sessionEnv":{%q:"x"}`, key), "")), false)
			if len(r.Problems) != 0 {
				t.Fatalf("problems = %q, want none: a secret-looking key is a warning only", r.Problems)
			}
			requireOneContaining(t, "warnings", r.Warnings, key)
		})
	}
}

func TestValidate_roleTypeWarnings(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	r := runServeValidate(t, writeRoleFile(t, ccpoolRole(pool, `,"strictBaseEnv":true`, "")), false)
	if len(r.Problems) != 0 {
		t.Fatalf("problems = %q", r.Problems)
	}
	requireOneContaining(t, "warnings", r.Warnings, "strictBaseEnv has no effect on a ccpool role")

	r = runServeValidate(t, writeRoleFile(t, `{"name":"c","type":"command","sessionEnv":{"A":"b"},"command":{"argv":["x"]}}`), false)
	if len(r.Problems) != 0 {
		t.Fatalf("problems = %q", r.Problems)
	}
	requireOneContaining(t, "warnings", r.Warnings, "sessionEnv has no effect on a command role")
}

func TestValidate_loadRulesAreProblems(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	cases := []struct{ name, body, want string }{
		{"empty actor", strings.Replace(ccpoolRole(pool, "", ""), `"actor":"r"`, `"actor":""`, 1), "ccpool.actor is required"},
		{"bad enum", strings.Replace(ccpoolRole(pool, "", ""), `"close-only"`, `"nope"`, 1), "invalid completion"},
		{"bad budget time", ccpoolRole(pool, "", `,"budget":{"time":"soon"}`), "budget.time"},
		{"unknown type", `{"name":"x","type":"weird"}`, "unknown type"},
		{"ccpool block missing", `{"name":"x","type":"ccpool"}`, "no [ccpool] block"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runServeValidate(t, writeRoleFile(t, tc.body), false)
			requireOneContaining(t, "problems", r.Problems, tc.want)
		})
	}
}

func TestValidate_poolDirectory(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	missing := filepath.Join(t.TempDir(), "no-such-pool")

	t.Run("missing poolDir is a problem", func(t *testing.T) {
		r := runServeValidate(t, writeRoleFile(t, ccpoolRole(missing, "", "")), false)
		requireOneContaining(t, "problems", r.Problems, missing)
	})
	t.Run("--skip-pool-dirs: neither problem nor warning", func(t *testing.T) {
		r := runServeValidate(t, writeRoleFile(t, ccpoolRole(missing, "", "")), true)
		if len(r.Problems) != 0 || len(r.Warnings) != 0 {
			t.Fatalf("problems=%q warnings=%q, want none", r.Problems, r.Warnings)
		}
	})
	t.Run("--skip-pool-dirs skips only the pool check", func(t *testing.T) {
		r := runServeValidate(t, writeRoleFile(t, ccpoolRole(missing, `,"nope":1`, "")), true)
		requireOneContaining(t, "problems", r.Problems, `unknown key "nope"`)
	})
	t.Run("a file is not a pool directory", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		r := runServeValidate(t, writeRoleFile(t, ccpoolRole(f, "", "")), false)
		requireOneContaining(t, "problems", r.Problems, "not a directory")
	})
	t.Run("CCPOOL_POOL applies when poolDir is unset", func(t *testing.T) {
		t.Setenv("CCPOOL_POOL", missing)
		body := strings.Replace(ccpoolRole("", "", ""), `,"poolDir":""`, "", 1)
		r := runServeValidate(t, writeRoleFile(t, body), false)
		requireOneContaining(t, "problems", r.Problems, "CCPOOL_POOL")
		t.Setenv("CCPOOL_POOL", t.TempDir())
		if r := runServeValidate(t, writeRoleFile(t, body), false); len(r.Problems) != 0 {
			t.Fatalf("an existing CCPOOL_POOL: problems = %q", r.Problems)
		}
	})
	t.Run("poolDir wins over CCPOOL_POOL", func(t *testing.T) {
		t.Setenv("CCPOOL_POOL", t.TempDir())
		r := runServeValidate(t, writeRoleFile(t, ccpoolRole(missing, "", "")), false)
		requireOneContaining(t, "problems", r.Problems, "ccpool.poolDir")
	})
	t.Run("no pool configured anywhere: nothing to check", func(t *testing.T) {
		body := strings.Replace(ccpoolRole("", "", ""), `,"poolDir":""`, "", 1)
		if r := runServeValidate(t, writeRoleFile(t, body), false); len(r.Problems) != 0 {
			t.Fatalf("problems = %q", r.Problems)
		}
	})
}

func TestValidate_neverReadsAWrapSpec(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	pool := t.TempDir()
	spec := filepath.Join(t.TempDir(), "does-not-exist.toml")
	r := runServeValidate(t, writeRoleFile(t, ccpoolRole(pool, fmt.Sprintf(`,"sessionEnv":{"PG_WRAP_SPEC":%q}`, spec), "")), false)
	if len(r.Problems) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("problems=%q warnings=%q: a missing PG_WRAP_SPEC file must not matter to validate", r.Problems, r.Warnings)
	}
}

func TestValidate_noRoleConfigIsAProblemNotUsage(t *testing.T) {
	r := runServeValidate(t, "", false) // serveValidate returns ExitOK or fails the test
	requireOneContaining(t, "problems", r.Problems, "no role config given")
}

func TestValidate_unreadableOrMalformedRoleFileIsAProblem(t *testing.T) {
	r := runServeValidate(t, filepath.Join(t.TempDir(), "absent.json"), false)
	requireOneContaining(t, "problems", r.Problems, "read role config")
	for name, body := range map[string]string{"malformed": `{"name":`, "not an object": `[1]`, "trailing data": `{} {}`} {
		r := runServeValidate(t, writeRoleFile(t, body), false)
		if len(r.Problems) != 1 {
			t.Errorf("%s: problems = %q, want exactly one", name, r.Problems)
		}
	}
}

func TestValidate_malformedStdinRequestIsAnErrorReply(t *testing.T) {
	for _, in := range []string{`{not json`, `{"schemaVersion":"1"}`} { // malformed JSON; schema-invalid (no id)
		var out bytes.Buffer
		code := serveValidate(strings.NewReader(in), &out, "", false)
		if code != conformance.ExitError {
			t.Fatalf("%q: exit = %d, want %d", in, code, conformance.ExitError)
		}
		var body map[string]any
		if err := json.Unmarshal(out.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["error"]; !ok {
			t.Errorf("%q: reply %s has no error", in, out.String())
		}
	}
}

// TestRunValidate_cliFlags covers the flag surface through run(): the
// subcommand is registered, --skip-pool-dirs and --role-config work, an unknown
// flag is a usage error, and the help documents --skip-pool-dirs.
func TestRunValidate_cliFlags(t *testing.T) {
	t.Setenv("CCPOOL_POOL", "")
	missing := filepath.Join(t.TempDir(), "no-such-pool")
	role := writeRoleFile(t, ccpoolRole(missing, "", ""))

	reply := func(args ...string) (int, validateReplyBody) {
		restore := redirectStdin(t, validateRequest)
		defer restore()
		var code int
		out := captureStdout(t, func() { code = run(append([]string{"validate"}, args...)) })
		var r validateReplyBody
		_ = json.Unmarshal([]byte(out), &r)
		return code, r
	}
	if code, r := reply("--role-config", role); code != 0 || len(r.Problems) != 1 {
		t.Fatalf("exit=%d problems=%q, want 0 and the missing-pool problem", code, r.Problems)
	}
	if code, r := reply("--role-config", role, "--skip-pool-dirs"); code != 0 || len(r.Problems) != 0 {
		t.Fatalf("exit=%d problems=%q, want 0 and none with --skip-pool-dirs", code, r.Problems)
	}
	t.Setenv(envRoleConfig, role)
	if code, r := reply(); code != 0 || len(r.Problems) != 1 {
		t.Fatalf("env fallback: exit=%d problems=%q", code, r.Problems)
	}
	if code, _ := reply("--bogus"); code != conformance.ExitUsage {
		t.Fatalf("unknown flag: exit = %d, want %d", code, conformance.ExitUsage)
	}
	if !strings.Contains(helpText, "--skip-pool-dirs") || !strings.Contains(usageLine, "validate") {
		t.Errorf("--help must document validate and --skip-pool-dirs")
	}
}

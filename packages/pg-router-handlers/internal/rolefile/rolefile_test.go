package rolefile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/budget"
	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
)

// TestLoadConfig_rejectsInvalidPermissionMode proves LoadConfig — this
// module's own config-decoding entrypoint (dispatch.go calls it on every
// dispatch) — rejects an invalid claude --permission-mode value AT ITS OWN
// config-load time (docket pg2-oju6w Task 5.7's acceptance criterion), by
// way of config.Config.Validate().
func TestLoadConfig_rejectsInvalidPermissionMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"permissionMode":"yolo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("LoadConfig with an invalid permissionMode must fail, got nil error")
	} else if !strings.Contains(err.Error(), "invalid permissionMode") {
		t.Errorf("LoadConfig error must name the invalid permissionMode; got %v", err)
	}
}

// TestLoadConfig_acceptsValidPermissionMode is the positive control: a real
// claude --permission-mode value decodes and validates cleanly.
func TestLoadConfig_acceptsValidPermissionMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"permissionMode":"plan"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig with a valid permissionMode must succeed: %v", err)
	}
	if cfg.PermissionMode != "plan" {
		t.Errorf("PermissionMode = %q, want plan", cfg.PermissionMode)
	}
}

// TestLoadConfig_ignoresRetiredPRToolKey proves a launch-config JSON written
// before the PR tool was retired (still carrying the removed "prTool" key) decodes
// cleanly and adds no Bash(<prTool>:*) grant (bead pg2-k9vmd): the removed
// field must not turn an un-migrated config into a decode failure.
func TestLoadConfig_ignoresRetiredPRToolKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"prTool":"legacy-pr-tool","permissionMode":"plan"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig with a retired prTool key must still succeed: %v", err)
	}
	if cfg.PermissionMode != "plan" {
		t.Errorf("PermissionMode = %q, want plan (other keys must still decode)", cfg.PermissionMode)
	}
	if strings.Contains(cfg.AllowedTools, "legacy-pr-tool") {
		t.Errorf("AllowedTools = %q, must carry no grant derived from the retired prTool key", cfg.AllowedTools)
	}
	if cfg.AllowedTools != config.Default().AllowedTools {
		t.Errorf("AllowedTools = %q, want the Default() allowlist unchanged", cfg.AllowedTools)
	}
}

// TestLoadConfig_emptyPathValidatesDefault proves the empty-path/Default()
// shortcut is validated too, not skipped.
func TestLoadConfig_emptyPathValidatesDefault(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig(\"\") must succeed against config.Default(): %v", err)
	}
	if cfg.PermissionMode != "dontAsk" {
		t.Errorf("PermissionMode = %q, want dontAsk (config.Default())", cfg.PermissionMode)
	}
}

// budgetRoleTemplate is a minimal valid ccpool RoleFile carrying a
// budget.tokens/budget.cost/budget.time block, used by the budget-decode
// tests below (pg2-r8al1).
const budgetRoleTemplate = `{
	"name": "worker",
		"ccpool": {
		"actor": "pgii-pool__worker",
		"completion": "close-or-handback",
		"onFailure": "add-human",
		"onDispatchFail": "leave",
		"promptBody": "do the work",
		"budget": {"tokens": 5000, "cost": 250, "time": "25m"}
	}
}`

// loadRoleFile decodes the role JSON file at path the way an adapter decodes a
// request's [role.config] (with the sessionEnv key rules applied).
func loadRoleFile(path string) (roles.Role, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return roles.Role{}, err
	}
	return DecodeRole(path, data, true)
}

func mustWriteRoleFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "role.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDecodeRole_decodesCCPoolBudget proves role JSON's
// budget.tokens/budget.cost/budget.time decode into
// roles.CCPoolConfig.Budget correctly (pg2-r8al1 acceptance criterion 1) —
// before this, RoleFile.CCPool carried no Budget field at all, so a role
// config could never populate anything but the unlimited Go zero value.
func TestDecodeRole_decodesCCPoolBudget(t *testing.T) {
	role, err := loadRoleFile(mustWriteRoleFile(t, budgetRoleTemplate))
	if err != nil {
		t.Fatalf("DecodeRole: %v", err)
	}
	got := role.CCPool.Budget
	if got.Tokens != 5000 {
		t.Errorf("Budget.Tokens = %v, want 5000", got.Tokens)
	}
	if got.Cost != 250 {
		t.Errorf("Budget.Cost = %v, want 250", got.Cost)
	}
	if got.Time != 25*time.Minute {
		t.Errorf("Budget.Time = %v, want 25m", got.Time)
	}
}

// TestDecodeRole_ccpoolBudgetAbsentIsUnlimited is the positive control: a
// RoleFile with no "budget" block at all still decodes cleanly to the
// unlimited zero value (today's unchanged behavior for a role that never
// wants a watchdog, e.g. "feedback").
func TestDecodeRole_ccpoolBudgetAbsentIsUnlimited(t *testing.T) {
	role, err := loadRoleFile(mustWriteRoleFile(t, `{
		"name": "feedback",
				"ccpool": {
			"actor": "pgii-pool__process-feedback",
			"completion": "close-only",
			"onFailure": "unclaim",
			"onDispatchFail": "unclaim",
			"promptBody": "process feedback"
		}
	}`))
	if err != nil {
		t.Fatalf("DecodeRole: %v", err)
	}
	b := role.CCPool.Budget
	if !b.Tokens.Unlimited() || !b.Cost.Unlimited() || b.Time > 0 {
		t.Errorf("no budget block should decode to the unlimited zero value, got %+v", b)
	}
}

// TestDecodeRole_decodesPoolDir proves role JSON's "poolDir" key
// decodes into roles.CCPoolConfig.PoolDir (bead pg2-mr0sl) — the per-role
// dedicated ccpool pool override buildDeps (dispatch.go) reads to scope this
// role's own CLIRunner via ccpool.NewCLIRunnerForPool.
func TestDecodeRole_decodesPoolDir(t *testing.T) {
	role, err := loadRoleFile(mustWriteRoleFile(t, `{
		"name": "review",
				"ccpool": {
			"actor": "pgii-pool__review",
			"completion": "close-only",
			"onFailure": "unclaim",
			"onDispatchFail": "unclaim",
			"promptBody": "review",
			"poolDir": "/state/pg-router-ccpool-review"
		}
	}`))
	if err != nil {
		t.Fatalf("DecodeRole: %v", err)
	}
	if role.CCPool.PoolDir != "/state/pg-router-ccpool-review" {
		t.Errorf("PoolDir = %q, want %q", role.CCPool.PoolDir, "/state/pg-router-ccpool-review")
	}
}

// TestDecodeRole_poolDirAbsentIsEmpty is the positive control: a RoleFile with
// no "poolDir" key at all still decodes cleanly to "" (today's unchanged
// behavior — buildDeps only overrides the pool when PoolDir is non-empty).
func TestDecodeRole_poolDirAbsentIsEmpty(t *testing.T) {
	role, err := loadRoleFile(mustWriteRoleFile(t, budgetRoleTemplate))
	if err != nil {
		t.Fatalf("DecodeRole: %v", err)
	}
	if role.CCPool.PoolDir != "" {
		t.Errorf("PoolDir = %q, want \"\" (no poolDir key in budgetRoleTemplate)", role.CCPool.PoolDir)
	}
}

// TestDecodeRole_ccpoolBudgetExplicitZeroTimeIsUnlimited proves an explicit
// `"time": "0s"` (the old schema's own way to deliberately request
// "unlimited, no watchdog" per role — the bug report's "feedback" example)
// decodes to Time == 0, not an error and not a nonzero duration.
func TestDecodeRole_ccpoolBudgetExplicitZeroTimeIsUnlimited(t *testing.T) {
	role, err := loadRoleFile(mustWriteRoleFile(t, `{
		"name": "feedback",
				"ccpool": {
			"actor": "pgii-pool__process-feedback",
			"completion": "close-only",
			"onFailure": "unclaim",
			"onDispatchFail": "unclaim",
			"promptBody": "process feedback",
			"budget": {"time": "0s"}
		}
	}`))
	if err != nil {
		t.Fatalf("DecodeRole: %v", err)
	}
	if role.CCPool.Budget.Time != 0 {
		t.Errorf("explicit budget.time=0s must decode to Time==0 (unlimited), got %v", role.CCPool.Budget.Time)
	}
}

// TestDecodeRole_ccpoolBudgetInvalidTimeErrors proves a malformed budget.time
// fails DecodeRole with an error naming budget.time, rather than silently
// zeroing it or panicking.
func TestDecodeRole_ccpoolBudgetInvalidTimeErrors(t *testing.T) {
	p := mustWriteRoleFile(t, `{
		"name": "worker",
				"ccpool": {
			"actor": "a",
			"completion": "close-or-handback",
			"onFailure": "add-human",
			"onDispatchFail": "leave",
			"promptBody": "do work",
			"budget": {"time": "not-a-duration"}
		}
	}`)
	if _, err := loadRoleFile(p); err == nil {
		t.Fatal("DecodeRole with an invalid budget.time must fail, got nil error")
	} else if !strings.Contains(err.Error(), "budget.time") {
		t.Errorf("DecodeRole error must name budget.time; got %v", err)
	}
}

// TestOverlayBudgetThresholds_ccpoolRoleGetsCfgThresholds proves
// OverlayBudgetThresholds fills a ccpool role's Budget.Thresholds from the
// launch cfg's pool-wide Reminder/Cancel/HardPct, without disturbing the
// per-role Tokens/Cost/Time DecodeRole already decoded — without this, a role
// with any finite budget would evaluate against a Thresholds{0,0,0} zero
// value and hard-stop on its very first watchdog tick (see the function's
// own doc comment).
func TestOverlayBudgetThresholds_ccpoolRoleGetsCfgThresholds(t *testing.T) {
	role, err := loadRoleFile(mustWriteRoleFile(t, budgetRoleTemplate))
	if err != nil {
		t.Fatalf("DecodeRole: %v", err)
	}
	cfg := config.Default()
	cfg.ReminderPct, cfg.CancelPct, cfg.HardPct = 0.5, 0.75, 0.95

	OverlayBudgetThresholds(role, cfg)

	want := budget.Thresholds{Reminder: 0.5, Cancel: 0.75, Hard: 0.95}
	if role.CCPool.Budget.Thresholds != want {
		t.Errorf("Thresholds = %+v, want %+v", role.CCPool.Budget.Thresholds, want)
	}
	if role.CCPool.Budget.Tokens != 5000 || role.CCPool.Budget.Time != 25*time.Minute {
		t.Errorf("overlay must not disturb per-role Tokens/Time, got %+v", role.CCPool.Budget)
	}
}

// TestOverlayBudgetThresholds_commandRoleNoop proves OverlayBudgetThresholds
// is a safe no-op for a command role (CCPool == nil), rather than panicking
// on a nil-pointer dereference.
func TestOverlayBudgetThresholds_commandRoleNoop(t *testing.T) {
	OverlayBudgetThresholds(roles.Role{Name: "worker"}, config.Default())
}

// TestDecodeRole_decodesBeadsDir proves the "beadsDir" key (bead pg2-2grpj)
// decodes into roles.CCPoolConfig.BeadsDir, alongside the close-or-triage
// completion the escalation triager uses.
func TestDecodeRole_decodesBeadsDir(t *testing.T) {
	role, err := loadRoleFile(mustWriteRoleFile(t, `{
		"name": "pg2-escalation-triager",
				"ccpool": {
			"actor": "pgii-pool__pg2-escalation-triager",
			"completion": "close-or-triage",
			"onFailure": "add-human",
			"onDispatchFail": "leave",
			"promptBody": "triage",
			"beadsDir": "/Users/x/pg2"
		}
	}`))
	if err != nil {
		t.Fatalf("DecodeRole: %v", err)
	}
	if role.CCPool.BeadsDir != "/Users/x/pg2" || role.CCPool.Completion != roles.CloseOrTriage {
		t.Errorf("BeadsDir=%q Completion=%q", role.CCPool.BeadsDir, role.CCPool.Completion)
	}
}

// TestDecodeRole_budgetStopEscalateAfter (bead pg2-6akgz): absent => 3, explicit 0
// => 0 (kill switch), explicit N => N, negative rejected; budget unchanged.
func TestDecodeRole_budgetStopEscalateAfter(t *testing.T) {
	mk := func(extra string) string {
		return `{"name":"worker","ccpool":{"actor":"a","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"p","budget":{"tokens":5000,"cost":250,"time":"25m"}` + extra + `}}`
	}
	for _, tc := range []struct {
		name, extra string
		want        int
		wantErr     bool
	}{
		{"absent defaults to 3", "", 3, false},
		{"explicit zero disables", `,"budgetStopEscalateAfter":0`, 0, false},
		{"explicit 5", `,"budgetStopEscalateAfter":5`, 5, false},
		{"negative rejected", `,"budgetStopEscalateAfter":-1`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role, err := loadRoleFile(mustWriteRoleFile(t, mk(tc.extra)))
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := role.CCPool.BudgetStopEscalateAfter; got != tc.want {
				t.Errorf("BudgetStopEscalateAfter = %d, want %d", got, tc.want)
			}
			b := role.CCPool.Budget
			if b.Tokens != 5000 || b.Cost != 250 || b.Time != 25*time.Minute {
				t.Errorf("budget must be unchanged by the threshold, got %+v", b)
			}
		})
	}
}

// pg2-lhi3b: an empty ccpool.actor would make bd fall back to git user.name
// (the operator) for the role's claims, so DecodeRole rejects it.
func TestDecodeRole_rejectsEmptyCCPoolActor(t *testing.T) {
	for _, actor := range []string{`""`, `"   "`} {
		body := `{"name":"worker","ccpool":{"actor":` + actor + `,"completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"p"}}`
		if _, err := loadRoleFile(mustWriteRoleFile(t, body)); err == nil || !strings.Contains(err.Error(), "ccpool.actor is required") {
			t.Errorf("actor %s: err = %v, want ccpool.actor is required", actor, err)
		}
	}
}

// prefetchRoleBody builds a ccpool RoleFile whose isolation block is iso
// (bead pg2-hh32y). The sub-field names are Go's own (roles.IsolationConfig
// carries no json tags), exactly what the nix module renders.
func prefetchRoleBody(iso string) string {
	return `{"name":"review","ccpool":{"actor":"a","completion":"close-or-handback",` +
		`"onFailure":"add-human","onDispatchFail":"leave","promptBody":"x","isolation":` + iso + `}}`
}

func TestDecodeRole_decodesIsolationPrefetch(t *testing.T) {
	iso := `{"Type":"worktree","Prefetch":{"Remote":"origin","Refspec":"pull/{{index .Item.Metadata \"pr_number\"}}/head",` +
		`"Rev":"{{index .Item.Metadata \"head_sha\"}}","DiffBase":"origin/main","Timeout":"3m"}}`
	r, err := loadRoleFile(mustWriteRoleFile(t, prefetchRoleBody(iso)))
	if err != nil {
		t.Fatalf("DecodeRole: %v", err)
	}
	pf := r.CCPool.Isolation.Prefetch
	if pf == nil || pf.Remote != "origin" || pf.DiffBase != "origin/main" || pf.Timeout != "3m" ||
		!strings.Contains(pf.Refspec, "pr_number") || !strings.Contains(pf.Rev, "head_sha") {
		t.Fatalf("Prefetch = %+v, want the configured fields", pf)
	}
}

func TestDecodeRole_prefetchAbsentIsNil(t *testing.T) {
	r, err := loadRoleFile(mustWriteRoleFile(t, prefetchRoleBody(`{"Type":"worktree"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if r.CCPool.Isolation.Prefetch != nil {
		t.Errorf("Prefetch = %+v, want nil when the block is absent", r.CCPool.Isolation.Prefetch)
	}
}

func TestDecodeRole_rejectsInvalidPrefetch(t *testing.T) {
	tests := map[string]string{
		"non-worktree isolation": `{"Type":"none","Prefetch":{"Rev":"abc1234"}}`,
		"path isolation":         `{"Type":"path","Path":"/x","Prefetch":{"Rev":"abc1234"}}`,
		"missing rev":            `{"Prefetch":{"Refspec":"x"}}`,
		"unparseable template":   `{"Prefetch":{"Rev":"{{"}}`,
		"bad timeout":            `{"Prefetch":{"Rev":"abc1234","Timeout":"soon"}}`,
	}
	for name, iso := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := loadRoleFile(mustWriteRoleFile(t, prefetchRoleBody(iso))); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

// TestDecodeRole_worktreeQuietWindow (bead pg2-uyahp, INV-CCH-23): absent => 0
// (no override: the handler default applies), a duration string => that value,
// a malformed, zero, or negative value is rejected.
func TestDecodeRole_worktreeQuietWindow(t *testing.T) {
	mk := func(extra string) string {
		return `{"name":"review","ccpool":{"actor":"a","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"p"` + extra + `}}`
	}
	for _, tc := range []struct {
		name, extra string
		want        time.Duration
		wantErr     bool
	}{
		{"absent is no override", "", 0, false},
		{"empty string is no override", `,"worktreeQuietWindow":""`, 0, false},
		{"30s", `,"worktreeQuietWindow":"30s"`, 30 * time.Second, false},
		{"1m30s", `,"worktreeQuietWindow":"1m30s"`, 90 * time.Second, false},
		{"malformed rejected", `,"worktreeQuietWindow":"soon"`, 0, true},
		{"zero rejected", `,"worktreeQuietWindow":"0s"`, 0, true},
		{"negative rejected", `,"worktreeQuietWindow":"-5s"`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role, err := loadRoleFile(mustWriteRoleFile(t, mk(tc.extra)))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "worktreeQuietWindow") {
					t.Fatalf("err = %v, want a worktreeQuietWindow error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := role.CCPool.WorktreeQuietWindow; got != tc.want {
				t.Errorf("WorktreeQuietWindow = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDecodeRole_needsInputStall (bead pg2-zybju, INV-CCH-29): absent => 0 (no
// override: the handler-wide bound applies), a duration string => that value, a
// malformed, zero, or negative value is rejected. The handler-wide 0 is the only
// kill switch, so a role cannot spell "off" with a zero.
func TestDecodeRole_needsInputStall(t *testing.T) {
	mk := func(extra string) string {
		return `{"name":"drain","ccpool":{"actor":"a","completion":"close-or-release","onFailure":"add-human","onDispatchFail":"leave","promptBody":"p"` + extra + `}}`
	}
	for _, tc := range []struct {
		name, extra string
		want        time.Duration
		wantErr     bool
	}{
		{"absent is no override", "", 0, false},
		{"empty string is no override", `,"needsInputStall":""`, 0, false},
		{"20m", `,"needsInputStall":"20m"`, 20 * time.Minute, false},
		{"1h30m", `,"needsInputStall":"1h30m"`, 90 * time.Minute, false},
		{"malformed rejected", `,"needsInputStall":"soon"`, 0, true},
		{"zero rejected", `,"needsInputStall":"0s"`, 0, true},
		{"negative rejected", `,"needsInputStall":"-5m"`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role, err := loadRoleFile(mustWriteRoleFile(t, mk(tc.extra)))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "needsInputStall") {
					t.Fatalf("err = %v, want a needsInputStall error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := role.CCPool.NeedsInputStall; got != tc.want {
				t.Errorf("NeedsInputStall = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLoadConfig_needsInputStall: the handler-wide bound is a nanosecond integer
// like every other Config duration; absent keeps the 15m default, 0 disables, and
// a value under the polling floor is rejected at load.
func TestLoadConfig_needsInputStall(t *testing.T) {
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	c, err := LoadConfig(write(`{}`))
	if err != nil || c.NeedsInputStall != 15*time.Minute {
		t.Fatalf("absent: %v, %v", c.NeedsInputStall, err)
	}
	c, err = LoadConfig(write(`{"NeedsInputStall":0}`))
	if err != nil || c.NeedsInputStall != 0 {
		t.Fatalf("zero must disable: %v, %v", c.NeedsInputStall, err)
	}
	c, err = LoadConfig(write(`{"NeedsInputStall":1200000000000}`))
	if err != nil || c.NeedsInputStall != 20*time.Minute {
		t.Fatalf("20m in nanoseconds: %v, %v", c.NeedsInputStall, err)
	}
	if _, err = LoadConfig(write(`{"NeedsInputStall":30000000000}`)); err == nil || !strings.Contains(err.Error(), "needsInputStall") {
		t.Fatalf("30s is under six polls and must be rejected naming the key, got %v", err)
	}
}

// TestDecodeRole_extraAllowedTools (bead pg2-nk6th.5): absent/empty => no extra
// grants; a list is carried through in order; a blank entry is rejected.
func TestDecodeRole_extraAllowedTools(t *testing.T) {
	mk := func(extra string) string {
		return `{"name":"zr-drain","ccpool":{"actor":"a","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"p"` + extra + `}}`
	}
	for _, tc := range []struct {
		name, extra string
		want        []string
		wantErr     bool
	}{
		{"absent", "", nil, false},
		{"empty list", `,"extraAllowedTools":[]`, []string{}, false},
		{"ordered list", `,"extraAllowedTools":["Skill","Bash(git push:*)","Bash(gh pr create:*)"]`, []string{"Skill", "Bash(git push:*)", "Bash(gh pr create:*)"}, false},
		{"blank entry rejected", `,"extraAllowedTools":["Skill"," "]`, nil, true},
		{"empty entry rejected", `,"extraAllowedTools":[""]`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role, err := loadRoleFile(mustWriteRoleFile(t, mk(tc.extra)))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "extraAllowedTools") {
					t.Fatalf("err = %v, want an extraAllowedTools error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := role.CCPool.ExtraAllowedTools
			if len(got) != len(tc.want) {
				t.Fatalf("ExtraAllowedTools = %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("ExtraAllowedTools[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestDecodeRole_precheckAndCloseOrRelease (bead pg2-nk6th.3): precheck is absent
// by default, accepts review and ready, and rejects anything else; the
// close-or-release completion loads.
func TestDecodeRole_precheckAndCloseOrRelease(t *testing.T) {
	mk := func(completion, extra string) string {
		return `{"name":"zr-drain","ccpool":{"actor":"a","completion":"` + completion + `","onFailure":"add-human","onDispatchFail":"leave","promptBody":"p"` + extra + `}}`
	}
	for _, tc := range []struct {
		name, completion, extra string
		want                    roles.Precheck
		wantErr                 string
	}{
		{"absent", "close-or-release", "", roles.PrecheckNone, ""},
		{"ready", "close-or-release", `,"precheck":"ready"`, roles.PrecheckReady, ""},
		{"review", "close-or-handback", `,"precheck":"review"`, roles.PrecheckReview, ""},
		{"triage", "close-or-triage", `,"precheck":"triage"`, roles.PrecheckTriage, ""},
		{"empty", "close-only", `,"precheck":""`, roles.PrecheckNone, ""},
		{"unknown value", "close-or-release", `,"precheck":"bogus"`, "", "invalid precheck"},
		{"unknown completion", "close-or-bogus", "", "", "invalid completion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role, err := loadRoleFile(mustWriteRoleFile(t, mk(tc.completion, tc.extra)))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if role.CCPool.Precheck != tc.want {
				t.Errorf("Precheck = %q, want %q", role.CCPool.Precheck, tc.want)
			}
			if string(role.CCPool.Completion) != tc.completion {
				t.Errorf("Completion = %q, want %q", role.CCPool.Completion, tc.completion)
			}
		})
	}
}

// TestDecodeRole_effort (bead pg2-svkob, INV-CCH-31): absent/empty => no
// override; each claude --effort level loads; anything else is rejected at
// load, naming the key and the valid levels, so a typo cannot survive until
// claude itself fails to start.
func TestDecodeRole_effort(t *testing.T) {
	mk := func(extra string) string {
		return `{"name":"zr-drain","ccpool":{"actor":"a","completion":"close-or-release","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"p"` + extra + `}}`
	}
	for _, tc := range []struct {
		name, extra, want string
		wantErr           bool
	}{
		{"absent", "", "", false},
		{"empty string", `,"effort":""`, "", false},
		{"low", `,"effort":"low"`, "low", false},
		{"medium", `,"effort":"medium"`, "medium", false},
		{"high", `,"effort":"high"`, "high", false},
		{"xhigh", `,"effort":"xhigh"`, "xhigh", false},
		{"max", `,"effort":"max"`, "max", false},
		{"unknown level rejected", `,"effort":"ultra"`, "", true},
		{"wrong case rejected", `,"effort":"High"`, "", true},
		{"whitespace rejected", `,"effort":" high"`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role, err := loadRoleFile(mustWriteRoleFile(t, mk(tc.extra)))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "effort") || !strings.Contains(err.Error(), "xhigh") {
					t.Fatalf("err = %v, want an effort error listing the valid levels", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := role.CCPool.Effort; got != tc.want {
				t.Errorf("Effort = %q, want %q", got, tc.want)
			}
		})
	}
}

const sessionEnvCCPoolBlock = `"ccpool":{"actor":"a","completion":"close-only","onFailure":"unclaim","onDispatchFail":"unclaim","promptBody":"p"}`

// INV-CCH-33: the role decodes sessionEnv (string map, default empty) onto
// roles.Role.
func TestDecodeRole_decodesSessionEnv(t *testing.T) {
	role, err := loadRoleFile(mustWriteRoleFile(t, `{"name":"w","sessionEnv":{"K12_PROBE":"yes","PG_WRAP_SPEC":"/x.toml","EMPTY":""},`+sessionEnvCCPoolBlock+`}`))
	if err != nil {
		t.Fatalf("DecodeRole: %v", err)
	}
	want := map[string]string{"K12_PROBE": "yes", "PG_WRAP_SPEC": "/x.toml", "EMPTY": ""}
	if !reflect.DeepEqual(role.SessionEnv, want) {
		t.Errorf("SessionEnv = %v, want %v", role.SessionEnv, want)
	}
}

func TestDecodeRole_sessionEnvAbsentIsEmpty(t *testing.T) {
	role, err := loadRoleFile(mustWriteRoleFile(t, `{"name":"w",`+sessionEnvCCPoolBlock+`}`))
	if err != nil {
		t.Fatalf("DecodeRole: %v", err)
	}
	if len(role.SessionEnv) != 0 {
		t.Errorf("SessionEnv = %v, want empty when absent", role.SessionEnv)
	}
}

// A sessionEnv key that collides with an adapter marker (including any
// PG_ROUTER_*), is empty, or contains = fails the load, naming the role file and
// the key. The collision test is case-sensitive.
func TestDecodeRole_rejectsBadSessionEnvKeys(t *testing.T) {
	for _, tc := range []struct{ name, key, wantKey string }{
		{"beads-actor", "BEADS_ACTOR", "BEADS_ACTOR"},
		{"beads-dir", "BEADS_DIR", "BEADS_DIR"},
		{"workspace-root", "WORKSPACE_ROOT", "WORKSPACE_ROOT"},
		{"pg-router-role", "PG_ROUTER_ROLE", "PG_ROUTER_ROLE"},
		{"pg-router-item-id", "PG_ROUTER_ITEM_ID", "PG_ROUTER_ITEM_ID"},
		{"pg-router-other", "PG_ROUTER_ANYTHING", "PG_ROUTER_ANYTHING"},
		{"pg-router-bare-prefix", "PG_ROUTER_", "PG_ROUTER_"},
		{"empty", "", `""`},
		{"equals", "A=B", "A=B"},
		{"leading-equals", "=X", "=X"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mustWriteRoleFile(t, `{"name":"w","sessionEnv":{"`+tc.key+`":"v"},`+sessionEnvCCPoolBlock+`}`)
			_, err := loadRoleFile(p)
			if err == nil {
				t.Fatalf("key %q: DecodeRole succeeded, want a load error", tc.key)
			}
			for _, want := range []string{p, "sessionEnv", tc.wantKey} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q must name %q", err, want)
				}
			}
		})
	}
}

// Env var names are case-sensitive, so a lowercase near-miss is a valid key.
func TestDecodeRole_sessionEnvCollisionIsCaseSensitive(t *testing.T) {
	for _, k := range []string{"beads_actor", "pg_router_role", "PG_ROUTE", "XPG_ROUTER_ROLE"} {
		if _, err := loadRoleFile(mustWriteRoleFile(t, `{"name":"w","sessionEnv":{"`+k+`":"v"},`+sessionEnvCCPoolBlock+`}`)); err != nil {
			t.Errorf("key %q must load: %v", k, err)
		}
	}
}

// A non-string sessionEnv value is a decode error (a string-to-string map).
func TestDecodeRole_sessionEnvNonStringValueFails(t *testing.T) {
	if _, err := loadRoleFile(mustWriteRoleFile(t, `{"name":"w","sessionEnv":{"K":1},`+sessionEnvCCPoolBlock+`}`)); err == nil {
		t.Error("a numeric sessionEnv value must fail to decode")
	}
}

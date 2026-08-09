package configrules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func writeConfig(t *testing.T, dir string, cfg Config) {
	t.Helper()
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "rules.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigRules_ApprovedCommand(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, Config{ApprovedCommands: []string{"mytool", "mytool2"}})
	r := NewFromFile(filepath.Join(dir, "rules.json"))
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "mytool test ./..."}),
	})
	if got.Decision != hookio.Approve {
		t.Errorf("mytool: got %s, want approve", got.Decision)
	}
}

func TestConfigRules_BlockedCommand(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, Config{BlockedCommands: []string{"my-self-apply", "my-self-upgrade"}})
	r := NewFromFile(filepath.Join(dir, "rules.json"))
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "my-self-apply"}),
	})
	if got.Decision != hookio.Reject {
		t.Errorf("my-self-apply: got %s, want reject", got.Decision)
	}
}

func TestConfigRules_ApprovedCommandWithEnvVars_Abstains(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, Config{ApprovedCommands: []string{"mytool", "pytool"}})
	r := NewFromFile(filepath.Join(dir, "rules.json"))
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "PYTHONSTARTUP=/evil.py bin/pytool run"}),
	})
	if got.Decision != hookio.Abstain {
		t.Errorf("pytool with env var: got %s, want abstain", got.Decision)
	}
}

// TestConfigRules_SegmentScan_BlockedInLaterSegment is the named regression for
// bypass #8 (config-rules segment scan). Evaluate parses the command and scans
// EVERY leaf, so a blocked command hidden behind an approved/unknown first segment
// of a compound is still caught (Reject) — it does not stop at the first leaf.
// Guards against a regression to checking only parsed[0] (pg2-t4uyx).
func TestConfigRules_SegmentScan_BlockedInLaterSegment(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, Config{ApprovedCommands: []string{"mytool"}, BlockedCommands: []string{"my-self-apply"}})
	r := NewFromFile(filepath.Join(dir, "rules.json"))
	// Blocked command as the SECOND segment behind an unknown OR an approved first
	// command. The approved-first cases (mytool …) are the tc-0j90a regression: the
	// old single-pass loop returned Approve on the approved leaf before ever reaching
	// the blocked leaf.
	blocked := []string{
		"git status && my-self-apply",
		"echo hi ; my-self-apply",
		"echo hi | my-self-apply",
		"mytool && my-self-apply",
		"mytool ; my-self-apply",
		"mytool | my-self-apply",
	}
	for _, cmd := range blocked {
		got := r.Evaluate(&hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd})})
		if got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s, want reject (segment scan must reach the later blocked leaf)", cmd, got.Decision)
		}
	}
}

func TestConfigRules_AbstainForUnknown(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, Config{ApprovedCommands: []string{"mytool"}, BlockedCommands: []string{"my-self-apply"}})
	r := NewFromFile(filepath.Join(dir, "rules.json"))
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "git status"}),
	})
	if got.Decision != hookio.Abstain {
		t.Errorf("git status: got %s, want abstain", got.Decision)
	}
}

func TestConfigRules_AbstainWhenFileAbsent(t *testing.T) {
	r := NewFromFile("/nonexistent/path/rules.json")
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(map[string]string{"command": "mytool test ./..."}),
	})
	if got.Decision != hookio.Abstain {
		t.Errorf("missing file: got %s, want abstain", got.Decision)
	}
}

func TestConfigRules_NonBashAbstains(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, Config{ApprovedCommands: []string{"mytool"}})
	r := NewFromFile(filepath.Join(dir, "rules.json"))
	got := r.Evaluate(&hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"path": "/foo"}),
	})
	if got.Decision != hookio.Abstain {
		t.Errorf("non-bash: got %s, want abstain", got.Decision)
	}
}

// TestLoad_ParsesKubectlBuildtoolsBlocks proves the loader parses the structured
// kubectl{}/buildtools{} sub-configs from the ZR fixture — the schema the kubectl
// and build-tools rules consume via DI (ADR 0033).
func TestLoad_ParsesKubectlBuildtoolsBlocks(t *testing.T) {
	cfg := Load("testdata/zr-rules.json")

	k := cfg.Kubectl
	if got := k.ExecutableAliases; len(got) != 1 || got[0] != "kc" {
		t.Errorf("ExecutableAliases = %v, want [kc]", got)
	}
	if len(k.ReadOnlyVerbs) != 3 {
		t.Errorf("ReadOnlyVerbs = %v, want 3 (wslogs/zrlog/wsfirstpod)", k.ReadOnlyVerbs)
	}
	if len(k.ExecVerbs) != 3 {
		t.Errorf("ExecVerbs = %v, want 3 (exe/shell/wsexec)", k.ExecVerbs)
	}
	if len(k.ScopedApproveVerbs) != 3 || len(k.PositionalWorkspaceVerbs) != 2 {
		t.Errorf("scoped=%v positional=%v, want 3 and 2", k.ScopedApproveVerbs, k.PositionalWorkspaceVerbs)
	}
	if k.ClusterEnvVar != "KC_CLUSTER" || k.DevWorkspacePrefix != "d-" {
		t.Errorf("clusterEnvVar=%q devWorkspacePrefix=%q, want KC_CLUSTER and d-", k.ClusterEnvVar, k.DevWorkspacePrefix)
	}
	if len(k.DevClusterPrefixes) != 2 || len(k.DevWorkspaceFlags) != 2 || len(k.NonDevAccounts) != 7 {
		t.Errorf("clusterPfx=%v wsFlags=%v nonDev=%v; want 2/2/7", k.DevClusterPrefixes, k.DevWorkspaceFlags, k.NonDevAccounts)
	}

	b := cfg.Buildtools
	if len(b.ApprovedTools) != 2 {
		t.Errorf("buildtools.ApprovedTools = %v, want 2 (prove/yath)", b.ApprovedTools)
	}
	if len(b.ApprovedScripts) != 5 {
		t.Errorf("buildtools.ApprovedScripts = %v, want 5", b.ApprovedScripts)
	}

	// The 5 migrated scripts MUST NOT remain in the flat approvedCommands.
	for _, cmd := range cfg.ApprovedCommands {
		switch cmd {
		case "zr-proto-regenerate.sh", "pre-merge-protobuf-check", "fix-ai-tools-ownership", "pre-merge-py-check", "generate-build-deps":
			t.Errorf("migrated script %q must be removed from flat approvedCommands", cmd)
		}
	}
}

// TestLoad_ParsesVerbScopedApprovals proves the verbScopedApprovals schema parses.
func TestLoad_ParsesVerbScopedApprovals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	body := `{"buildtools":{"verbScopedApprovals":[{"tool":"mytool","verb":"check"}]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Load(path)
	if len(cfg.Buildtools.VerbScopedApprovals) != 1 ||
		cfg.Buildtools.VerbScopedApprovals[0].Tool != "mytool" ||
		cfg.Buildtools.VerbScopedApprovals[0].Verb != "check" {
		t.Errorf("VerbScopedApprovals = %+v, want [{mytool check}]", cfg.Buildtools.VerbScopedApprovals)
	}
}

// TestLoad_ParsesBuildtoolsFlagFields proves the two flag fields parse from JSON,
// and — the part that matters for tc-080p — that an EMPTY allowedFlags list is
// preserved as a present key. The buildtools rule keys strict flag checking on
// the key's presence, so a loader that dropped an empty list would silently leave
// the tool permissive.
func TestLoad_ParsesBuildtoolsFlagFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	body := `{"buildtools":{"valueFlags":{"mytool":["-f","--set:2"]},` +
		`"allowedFlags":{"mytool":["--quiet"],"strictool":[]}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Load(path)
	if got := cfg.Buildtools.ValueFlags["mytool"]; len(got) != 2 || got[0] != "-f" || got[1] != "--set:2" {
		t.Errorf("valueFlags[mytool] = %v, want [-f --set:2]", got)
	}
	if got := cfg.Buildtools.AllowedFlags["mytool"]; len(got) != 1 || got[0] != "--quiet" {
		t.Errorf("allowedFlags[mytool] = %v, want [--quiet]", got)
	}
	got, ok := cfg.Buildtools.AllowedFlags["strictool"]
	if !ok {
		t.Error("allowedFlags[strictool] key missing — an empty list MUST survive as a present key")
	}
	if len(got) != 0 {
		t.Errorf("allowedFlags[strictool] = %v, want empty", got)
	}
}

// TestLoad_ParsesCommandAwareBlocks proves the loader parses the structured
// ssh/vault/curl/monorepo sub-configs (WS3) — the schema the ssh, vault, curl,
// and monorepo rules consume via DI (ADR 0033).
func TestLoad_ParsesCommandAwareBlocks(t *testing.T) {
	cfg := Load("testdata/command-blocks-rules.json")

	if len(cfg.Ssh.AllowedUsers) != 1 || cfg.Ssh.AllowedUsers[0] != "deploy" {
		t.Errorf("ssh.AllowedUsers = %v, want [deploy]", cfg.Ssh.AllowedUsers)
	}
	if len(cfg.Ssh.ReadonlyCommands) != 3 {
		t.Errorf("ssh.ReadonlyCommands = %v, want 3", cfg.Ssh.ReadonlyCommands)
	}
	if subs := cfg.Ssh.ReadonlySubcommands["systemctl"]; len(subs) != 2 {
		t.Errorf("ssh.ReadonlySubcommands[systemctl] = %v, want 2", subs)
	}
	if flags := cfg.Ssh.DangerousInlineFlags["journalctl"]; len(flags) != 2 || flags[0] != "--vacuum-size" || flags[1] != "--rotate" {
		t.Errorf("ssh.DangerousInlineFlags[journalctl] = %v, want [--vacuum-size --rotate]", flags)
	}
	if flags := cfg.Ssh.DangerousInlineFlags["sed"]; len(flags) != 1 || flags[0] != "-i" {
		t.Errorf("ssh.DangerousInlineFlags[sed] = %v, want [-i]", flags)
	}
	if len(cfg.Ssh.SecretPathPatterns) != 3 || len(cfg.Ssh.PasswordFlagPatterns) != 2 {
		t.Errorf("ssh secret=%v passwd=%v; want 3 and 2", cfg.Ssh.SecretPathPatterns, cfg.Ssh.PasswordFlagPatterns)
	}

	if len(cfg.Vault.ReadVerbs) != 4 || len(cfg.Vault.WriteVerbs) != 3 {
		t.Errorf("vault read=%v write=%v; want 4 and 3", cfg.Vault.ReadVerbs, cfg.Vault.WriteVerbs)
	}

	if len(cfg.Curl.AllowedDomainSuffixes) != 2 {
		t.Errorf("curl.AllowedDomainSuffixes = %v, want 2", cfg.Curl.AllowedDomainSuffixes)
	}
	if len(cfg.Curl.DomainMethods) != 1 || cfg.Curl.DomainMethods[0].DomainSuffix != ".internal.example" ||
		len(cfg.Curl.DomainMethods[0].Methods) != 2 {
		t.Errorf("curl.DomainMethods = %+v, want one .internal.example with 2 methods", cfg.Curl.DomainMethods)
	}

	if len(cfg.Monorepo.ApprovedCommands) != 2 {
		t.Errorf("monorepo.ApprovedCommands = %v, want 2", cfg.Monorepo.ApprovedCommands)
	}
	if vars := cfg.Monorepo.DangerousEnvByWrapper["tc"]; len(vars) != 1 || vars[0] != "TC_DANGER" {
		t.Errorf("monorepo.DangerousEnvByWrapper[tc] = %v, want [TC_DANGER]", vars)
	}
}

// TestLoad_AbsentAndMalformed returns a zero Config (base behavior) safely.
func TestLoad_AbsentAndMalformed(t *testing.T) {
	if cfg := Load("/nonexistent/rules.json"); cfg == nil || len(cfg.Kubectl.ExecutableAliases) != 0 {
		t.Errorf("absent file: want zero Config, got %+v", cfg)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg := Load(path); cfg == nil || len(cfg.ApprovedCommands) != 0 {
		t.Errorf("malformed file: want zero Config, got %+v", cfg)
	}
}

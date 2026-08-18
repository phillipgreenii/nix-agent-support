package pathsafety

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
)

// --- ADR 0049: plugin hooks execution-path carve-out ---------------------------
//
// ADR 0041 leaves the whole `.claude/plugins/**` subtree approved (a plugin
// checkout is a large tree of legitimately-written files). ADR 0049 narrows an
// EXECUTION-PATH exception back out of that approval: a plugin's own
// `hooks/hooks.json` manifest, and the scripts it names via
// `${CLAUDE_PLUGIN_ROOT}`. Everything else in the subtree — cache/,
// installed_plugins.json, known_marketplaces.json, blocklist.json, and any
// marketplace content that is not a hook script — MUST stay approved, because
// that churn is a normal side effect of using Claude Code (the same collateral
// ADR 0041 already rejected a subtree-wide denyWrite over).

// --- pluginHooksDir: the directory-matching predicate, unit-tested directly ---

func TestPluginHooksDir(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantDir string
		wantOK  bool
	}{
		{
			"real layout: marketplaces/<mkt>/plugins/<name>/hooks/hooks.json",
			"/home/user/.claude/plugins/marketplaces/mkt/plugins/name/hooks/hooks.json",
			"/home/user/.claude/plugins/marketplaces/mkt/plugins/name/hooks",
			true,
		},
		{
			"real layout: marketplaces/.../hooks/ nested script",
			"/home/user/.claude/plugins/marketplaces/mkt/plugins/name/hooks/lib/foo.sh",
			"/home/user/.claude/plugins/marketplaces/mkt/plugins/name/hooks",
			true,
		},
		{
			"real layout: cache/<mkt>/<name>/<version>/hooks/hooks.json",
			"/home/user/.claude/plugins/cache/mkt/name/1.0.0/hooks/hooks.json",
			"/home/user/.claude/plugins/cache/mkt/name/1.0.0/hooks",
			true,
		},
		{
			"the hooks dir itself",
			"/home/user/.claude/plugins/marketplaces/mkt/plugins/name/hooks",
			"/home/user/.claude/plugins/marketplaces/mkt/plugins/name/hooks",
			true,
		},
		{
			"no plugin-root component between plugins and hooks",
			"/home/user/.claude/plugins/hooks/hooks.json",
			"", false,
		},
		{
			"hooks dir not under .claude/plugins at all (ccpool's own plugin)",
			"/repo/packages/ccpool/ccpool-plugin/hooks/hooks.json",
			"", false,
		},
		{
			"plugins is not immediately inside .claude",
			"/home/user/.claude/skills/plugins/name/hooks/hooks.json",
			"", false,
		},
		{
			"ordinary plugin content with no hooks ancestor at all",
			"/home/user/.claude/plugins/cache/mkt/name/1.0.0/installed_plugins.json",
			"", false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotDir, gotOK := pluginHooksDir(filepath.Clean(tc.path))
			if gotOK != tc.wantOK {
				t.Fatalf("pluginHooksDir(%q) ok = %v, want %v", tc.path, gotOK, tc.wantOK)
			}
			if gotOK && gotDir != tc.wantDir {
				t.Errorf("pluginHooksDir(%q) dir = %q, want %q", tc.path, gotDir, tc.wantDir)
			}
		})
	}
}

// --- resolvePluginHookScripts: parses real hooks.json shapes seen on this machine ---

func TestResolvePluginHookScripts_RealShapes(t *testing.T) {
	t.Run("single quoted CLAUDE_PLUGIN_ROOT reference (pg-pr's shape)", func(t *testing.T) {
		dir := t.TempDir()
		hooksDir := filepath.Join(dir, "hooks")
		mustMkdirAll(t, hooksDir)
		manifest := filepath.Join(hooksDir, "hooks.json")
		mustWriteFile(t, manifest, `{
			"hooks": {
				"PreToolUse": [
					{"matcher": "Bash", "hooks": [
						{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/require-agent-pr-comment-marker.sh", "timeout": 5}
					]}
				]
			}
		}`)
		got, err := resolvePluginHookScripts(manifest, dir)
		if err != nil {
			t.Fatalf("resolvePluginHookScripts: %v", err)
		}
		want := filepath.Join(dir, "hooks", "require-agent-pr-comment-marker.sh")
		if len(got) != 1 || got[0] != want {
			t.Errorf("got %v, want [%q]", got, want)
		}
	})

	t.Run("multiple space-separated references in one command (security-guidance's shape)", func(t *testing.T) {
		dir := t.TempDir()
		hooksDir := filepath.Join(dir, "hooks")
		mustMkdirAll(t, hooksDir)
		manifest := filepath.Join(hooksDir, "hooks.json")
		mustWriteFile(t, manifest, `{
			"hooks": {
				"SessionStart": [
					{"hooks": [
						{"type": "command", "command": "bash \"${CLAUDE_PLUGIN_ROOT}/hooks/sg-python.sh\" \"${CLAUDE_PLUGIN_ROOT}/hooks/ensure_agent_sdk.py\"", "timeout": 180}
					]}
				]
			}
		}`)
		got, err := resolvePluginHookScripts(manifest, dir)
		if err != nil {
			t.Fatalf("resolvePluginHookScripts: %v", err)
		}
		want := map[string]bool{
			filepath.Join(dir, "hooks", "sg-python.sh"):        true,
			filepath.Join(dir, "hooks", "ensure_agent_sdk.py"): true,
		}
		if len(got) != len(want) {
			t.Fatalf("got %v, want exactly %v", got, want)
		}
		for _, g := range got {
			if !want[g] {
				t.Errorf("unexpected script %q", g)
			}
		}
	})

	t.Run("bare command with no CLAUDE_PLUGIN_ROOT reference names nothing (beads' / ceta's shape)", func(t *testing.T) {
		dir := t.TempDir()
		hooksDir := filepath.Join(dir, "hooks")
		mustMkdirAll(t, hooksDir)
		manifest := filepath.Join(hooksDir, "hooks.json")
		mustWriteFile(t, manifest, `{
			"hooks": {
				"SessionStart": [{"hooks": [{"type": "command", "command": "bd codex-hook SessionStart"}]}],
				"PreToolUse":   [{"hooks": [{"type": "command", "command": "claude-extended-tool-approver", "timeout": 5}]}]
			}
		}`)
		got, err := resolvePluginHookScripts(manifest, dir)
		if err != nil {
			t.Fatalf("resolvePluginHookScripts: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("bare commands named %v, want none", got)
		}
	})

	t.Run("manifest absent reports the absent sentinel, not a generic error", func(t *testing.T) {
		dir := t.TempDir()
		_, err := resolvePluginHookScripts(filepath.Join(dir, "hooks", "hooks.json"), dir)
		if err != errPluginManifestAbsent {
			t.Errorf("err = %v, want errPluginManifestAbsent", err)
		}
	})

	t.Run("malformed JSON is a real error, not the absent sentinel", func(t *testing.T) {
		dir := t.TempDir()
		hooksDir := filepath.Join(dir, "hooks")
		mustMkdirAll(t, hooksDir)
		manifest := filepath.Join(hooksDir, "hooks.json")
		mustWriteFile(t, manifest, `{ not valid json`)
		_, err := resolvePluginHookScripts(manifest, dir)
		if err == nil {
			t.Fatal("want a parse error, got nil")
		}
		if err == errPluginManifestAbsent {
			t.Error("malformed JSON must not be reported as the absent sentinel — that sentinel is the ONE case callers must NOT fail-safe on")
		}
	})
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

// --- End-to-end through Rule.Evaluate ------------------------------------------

// newPluginFixture lays out a project with one plugin at
// <project>/.claude/plugins/marketplaces/mkt/plugins/<name>/hooks/hooks.json,
// containing manifestBody (if non-empty), and returns the project dir and the
// plugin's hooks dir.
func newPluginFixture(t *testing.T, name, manifestBody string) (project, hooksDir string) {
	t.Helper()
	project = t.TempDir()
	hooksDir = filepath.Join(project, ".claude", "plugins", "marketplaces", "mkt", "plugins", name, "hooks")
	mustMkdirAll(t, hooksDir)
	if manifestBody != "" {
		mustWriteFile(t, filepath.Join(hooksDir, "hooks.json"), manifestBody)
	}
	return project, hooksDir
}

func TestPathSafety_WritePluginHooksJSON_Abstain(t *testing.T) {
	project, hooksDir := newPluginFixture(t, "myplugin", `{"hooks":{}}`)
	manifest := filepath.Join(hooksDir, "hooks.json")
	pe := patheval.New(project)
	r := New(pe)
	if !pe.Evaluate(manifest).CanWrite() {
		t.Fatalf("precondition: %s is not in a writable zone, so this case cannot show the carve-out is load-bearing", manifest)
	}
	for _, tool := range []string{"Write", "Edit", "MultiEdit", "Delete"} {
		t.Run(tool, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(writeInput(tool, manifest, project)))
			if got.Decision != hookio.NoOpinion {
				t.Errorf("%s %s: got %s (%s), want abstain (ADR 0049)", tool, manifest, got.Decision, got.Reason)
			}
		})
	}
}

func TestPathSafety_WritePluginNamedScript_Abstain(t *testing.T) {
	project, hooksDir := newPluginFixture(t, "myplugin", `{
		"hooks": {"PreToolUse": [{"hooks": [
			{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/guard.sh"}
		]}]}
	}`)
	script := filepath.Join(hooksDir, "guard.sh")
	// The script does not need to exist on disk yet — the manifest names it by
	// path, and CETA is evaluating a WRITE that may be creating it for the first
	// time. Resolution must not depend on the target already existing.
	pe := patheval.New(project)
	r := New(pe)
	if !pe.Evaluate(script).CanWrite() {
		t.Fatalf("precondition: %s is not in a writable zone", script)
	}
	got := hookio.Verdict(r.Evaluate(writeInput("Write", script, project)))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("Write %s: got %s (%s), want abstain (ADR 0049 — script named by hooks.json)", script, got.Decision, got.Reason)
	}
}

func TestPathSafety_WritePluginUnnamedScript_Approve(t *testing.T) {
	project, hooksDir := newPluginFixture(t, "myplugin", `{
		"hooks": {"PreToolUse": [{"hooks": [
			{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/guard.sh"}
		]}]}
	}`)
	// A second script sitting in the same hooks/ directory that hooks.json does
	// NOT name (a helper library, a README) must stay approved — the rule matches
	// named scripts, not "anything in a hooks/ directory".
	other := filepath.Join(hooksDir, "README.md")
	pe := patheval.New(project)
	r := New(pe)
	got := hookio.Verdict(r.Evaluate(writeInput("Write", other, project)))
	if got.Decision != hookio.Approve {
		t.Errorf("Write %s: got %s (%s), want approve (not named by hooks.json)", other, got.Decision, got.Reason)
	}
}

// Ordinary plugin churn — the traffic ADR 0041 already refused to break with a
// subtree rule, and ADR 0049 explicitly rejected repeating that mistake for.
func TestPathSafety_WritePluginOrdinaryChurn_Approve(t *testing.T) {
	project := t.TempDir()
	pluginsRoot := filepath.Join(project, ".claude", "plugins")
	cases := []struct {
		name string
		path string
	}{
		{"cache churn", filepath.Join(pluginsRoot, "cache", "mkt", "name", "1.0.0", "plugin.json")},
		{"installed_plugins.json", filepath.Join(pluginsRoot, "installed_plugins.json")},
		{"known_marketplaces.json", filepath.Join(pluginsRoot, "known_marketplaces.json")},
		{"blocklist.json", filepath.Join(pluginsRoot, "blocklist.json")},
		{"marketplace manifest (no hooks ancestor)", filepath.Join(pluginsRoot, "marketplaces", "mkt", ".claude-plugin", "marketplace.json")},
		{"plugin's own non-hooks file", filepath.Join(pluginsRoot, "marketplaces", "mkt", "plugins", "name", "plugin.json")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe := patheval.New(project)
			r := New(pe)
			got := hookio.Verdict(r.Evaluate(writeInput("Write", tc.path, project)))
			if got.Decision != hookio.Approve {
				t.Errorf("Write %s: got %s (%s), want approve — ordinary plugin churn, ADR 0049 must not become a subtree rule", tc.path, got.Decision, got.Reason)
			}
		})
	}
}

// FAIL-SAFE: a manifest that exists but cannot be parsed must not let a candidate
// script silently fall through to approve.
func TestPathSafety_WritePluginUnparseableManifest_FailSafeAbstain(t *testing.T) {
	project, hooksDir := newPluginFixture(t, "myplugin", `{ this is not valid json`)
	cases := []string{
		filepath.Join(hooksDir, "hooks.json"),
		filepath.Join(hooksDir, "some-script.sh"),
		filepath.Join(hooksDir, "lib", "nested.sh"),
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			pe := patheval.New(project)
			r := New(pe)
			got := hookio.Verdict(r.Evaluate(writeInput("Write", p, project)))
			if got.Decision != hookio.NoOpinion {
				t.Errorf("Write %s: got %s (%s), want abstain — unparseable manifest must fail SAFE, not silently approve", p, got.Decision, got.Reason)
			}
		})
	}
}

// The fail-safe is scoped to the plugin's hooks/ directory, not the whole plugin —
// a sibling file outside hooks/ is unaffected by that same broken manifest.
func TestPathSafety_WritePluginUnparseableManifest_ScopedToHooksDir(t *testing.T) {
	project, hooksDir := newPluginFixture(t, "myplugin", `{ this is not valid json`)
	pluginRoot := filepath.Dir(hooksDir)
	sibling := filepath.Join(pluginRoot, "plugin.json")
	pe := patheval.New(project)
	r := New(pe)
	got := hookio.Verdict(r.Evaluate(writeInput("Write", sibling, project)))
	if got.Decision != hookio.Approve {
		t.Errorf("Write %s: got %s (%s), want approve — the broken manifest's fail-safe must not widen into a subtree rule", sibling, got.Decision, got.Reason)
	}
}

// No manifest at all (a plugin that ships no hooks) — ordinary content in what
// would be its hooks/ directory stays approved; there is nothing to fail safe about.
func TestPathSafety_WritePluginNoManifest_Approve(t *testing.T) {
	project, hooksDir := newPluginFixture(t, "myplugin", "")
	p := filepath.Join(hooksDir, "readme.txt")
	pe := patheval.New(project)
	r := New(pe)
	got := hookio.Verdict(r.Evaluate(writeInput("Write", p, project)))
	if got.Decision != hookio.Approve {
		t.Errorf("Write %s: got %s (%s), want approve — no manifest shipped, nothing named", p, got.Decision, got.Reason)
	}
}

// The verdict is Abstain specifically, never Ask or Reject (ADR 0041's stance,
// which ADR 0049 does not revisit).
func TestPathSafety_WritePluginHooksJSON_EncodesNoVerdict(t *testing.T) {
	project, hooksDir := newPluginFixture(t, "myplugin", `{"hooks":{}}`)
	manifest := filepath.Join(hooksDir, "hooks.json")
	pe := patheval.New(project)
	r := New(pe)
	got := hookio.Verdict(r.Evaluate(writeInput("Write", manifest, project)))
	if got.Decision == hookio.Ask || got.Decision == hookio.Reject {
		t.Errorf("plugin hooks.json write: got %s, want abstain — CETA must not encode a verdict of its own", got.Decision)
	}
}

// Reads are unaffected — the new checks live only in the Write/Edit/MultiEdit/Delete
// branch of Evaluate.
func TestPathSafety_ReadPluginHooksJSON_StillApprove(t *testing.T) {
	project, hooksDir := newPluginFixture(t, "myplugin", `{"hooks":{}}`)
	manifest := filepath.Join(hooksDir, "hooks.json")
	pe := patheval.New(project)
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": manifest}),
		CWD:       project,
	}
	got := hookio.Verdict(r.Evaluate(input))
	if got.Decision != hookio.Approve {
		t.Errorf("Read %s: got %s (%s), want approve (reads are unaffected)", manifest, got.Decision, got.Reason)
	}
}

// A symlink pointing INTO a plugin's named script must not slip the write past
// this check, mirroring the equivalent ADR 0041 symlink test.
func TestPathSafety_WritePluginNamedScriptViaSymlink_Abstain(t *testing.T) {
	project, hooksDir := newPluginFixture(t, "myplugin", `{
		"hooks": {"PreToolUse": [{"hooks": [
			{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/hooks/guard.sh"}
		]}]}
	}`)
	target := filepath.Join(hooksDir, "guard.sh")
	mustWriteFile(t, target, "#!/bin/sh\necho hi\n")
	link := filepath.Join(project, "my-script.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	pe := patheval.New(project)
	r := New(pe)
	got := hookio.Verdict(r.Evaluate(writeInput("Write", link, project)))
	if got.Decision != hookio.NoOpinion {
		t.Errorf("Write via symlink to named plugin script: got %s (%s), want abstain", got.Decision, got.Reason)
	}
}

// TestADR0049_PathSafety_PluginHooksSiteStaysTerminal pins the same boundary
// TestADR0044_PathSafety_AgentConfigSiteStaysTerminal pins for the ADR 0041 site:
// this verdict must be a TERMINAL NoOpinion with a nil error, not a refusal that
// continues the chain — ADR 0041's stance, which this carve-out inherits unchanged.
func TestADR0049_PathSafety_PluginHooksSiteStaysTerminal(t *testing.T) {
	project, hooksDir := newPluginFixture(t, "myplugin", `{"hooks":{}}`)
	manifest := filepath.Join(hooksDir, "hooks.json")
	pe := patheval.New(project)
	r := New(pe)
	input := &hookio.HookInput{
		ToolName:  "Write",
		ToolInput: mustJSON(map[string]string{"file_path": manifest, "content": "{}"}),
		CWD:       project,
	}
	res, err := r.Evaluate(input)
	if err != nil {
		t.Fatalf("plugin hooks.json write returned err=%v; want a TERMINAL verdict (nil error)", err)
	}
	if res.Decision != hookio.NoOpinion {
		t.Errorf("plugin hooks.json write = %s, want abstain", res.Decision)
	}
}

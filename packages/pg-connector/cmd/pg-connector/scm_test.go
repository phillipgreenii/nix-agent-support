package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// writeCwdEchoingBackend writes a fake backend that echoes the incoming
// request's raw JSON straight back as its own "result" — used only to
// assert which "cwd" wire arg branch detect sent, without needing a real
// scm backend (that's the sibling "pg-connector-scm-git" packet's job).
func writeCwdEchoingBackend(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, name)
	content := "#!/bin/sh\nreq=$(cat)\nprintf '{\"protocolVersion\":1,\"schemaVersion\":1,\"result\":%s}' \"$req\"\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake backend: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writeScmConfigFor writes a connector.scm config pointing at backend — a
// single scalar value, unlike writeConfigFor's list-valued connector.pr
// (see registry.go: connector.scm is single-valued) — and points
// $PG_PR_CONFIG at it.
func writeScmConfigFor(t *testing.T, backend string) {
	t.Helper()
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector:\n  scm: "+backend+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)
}

func TestRun_ScmWorktreeAdd_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-worktree-add", map[string]string{
		"worktree_add": `{"protocolVersion":1,"schemaVersion":1,"result":{"path":"/w/feature","branch":"feature","ref":"feature"}}`,
	}, `{}`)
	writeScmConfigFor(t, "backend-worktree-add")

	stdout, code := executePr(t, []string{"scm", "worktree", "add", "feature"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var info schema.WorktreeInfo
	if err := scriptout.Decode(resp.Result, &info); err != nil {
		t.Fatalf("decode WorktreeInfo: %v", err)
	}
	if info.Path != "/w/feature" || info.Branch != "feature" || info.Ref != "feature" {
		t.Fatalf("info = %+v", info)
	}
}

func TestRun_ScmWorktreeRemove_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-worktree-remove-ok", map[string]string{
		"worktree_remove": `{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	}, `{}`)
	writeScmConfigFor(t, "backend-worktree-remove-ok")

	stdout, code := executePr(t, []string{"scm", "worktree", "remove", "/w/feature"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	if resp.Error != nil {
		t.Fatalf("resp.Error = %+v, want nil", resp.Error)
	}
}

func TestRun_ScmWorktreeRemove_NotFound_Exit4(t *testing.T) {
	// A not_found response (path is not a known worktree) is a well-formed
	// negative answer under the targeted-op scheme (CLI exit 4), not a
	// broken call [design: §4.5, §4.7].
	writeOpAwareFakeBackend(t, "backend-worktree-remove-notfound", map[string]string{
		"worktree_remove": `{"protocolVersion":1,"schemaVersion":1,"error":{"code":"not_found","message":"worktree /w/missing not found"}}`,
	}, `{}`)
	writeScmConfigFor(t, "backend-worktree-remove-notfound")

	stdout, code := executePr(t, []string{"scm", "worktree", "remove", "/w/missing"})
	if code != 4 {
		t.Fatalf("exit code = %d, want 4; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	if resp.Error == nil || resp.Error.Code != "not_found" {
		t.Fatalf("resp.Error = %+v", resp.Error)
	}
}

func TestRun_ScmWorktreeList_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-worktree-list", map[string]string{
		"worktree_list": `{"protocolVersion":1,"schemaVersion":1,"result":[{"path":"/w/a","branch":"a","ref":"a"},{"path":"/w/b","branch":"b","ref":"b"}]}`,
	}, `{}`)
	writeScmConfigFor(t, "backend-worktree-list")

	stdout, code := executePr(t, []string{"scm", "worktree", "list"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var infos []schema.WorktreeInfo
	if err := scriptout.Decode(resp.Result, &infos); err != nil {
		t.Fatalf("decode []WorktreeInfo: %v", err)
	}
	if len(infos) != 2 || infos[0].Path != "/w/a" || infos[1].Path != "/w/b" {
		t.Fatalf("infos = %+v", infos)
	}
}

func TestRun_ScmBranchDetect_ExplicitCwd_Success(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-branch-detect", map[string]string{
		"branch_detect": `{"protocolVersion":1,"schemaVersion":1,"result":{"repo":"owner/repo","branch":"feature"}}`,
	}, `{}`)
	writeScmConfigFor(t, "backend-branch-detect")

	stdout, code := executePr(t, []string{"scm", "branch", "detect", "/home/u/repo"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}

	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var info schema.BranchInfo
	if err := scriptout.Decode(resp.Result, &info); err != nil {
		t.Fatalf("decode BranchInfo: %v", err)
	}
	if info.Repo != "owner/repo" || info.Branch != "feature" {
		t.Fatalf("info = %+v", info)
	}
}

func TestRun_ScmBranchDetect_DefaultsToProcessCwd(t *testing.T) {
	// With no cwd argument, branch detect must pass the process's own
	// working directory as the "cwd" wire arg.
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Fatalf("restore Chdir: %v", err)
		}
	}()
	realDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd (post-chdir): %v", err)
	}

	writeCwdEchoingBackend(t, "backend-branch-detect-default")
	writeScmConfigFor(t, "backend-branch-detect-default")

	stdout, code := executePr(t, []string{"scm", "branch", "detect"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	var resp scriptout.Response
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode response: %v (stdout=%s)", err, stdout)
	}
	var echoed struct {
		Op   string `json:"op"`
		Args struct {
			Cwd string `json:"cwd"`
		} `json:"args"`
	}
	if err := json.Unmarshal(resp.Result, &echoed); err != nil {
		t.Fatalf("decode echoed request: %v (result=%s)", resp.Result, err)
	}
	if echoed.Args.Cwd != realDir {
		t.Fatalf("echoed cwd = %q, want %q", echoed.Args.Cwd, realDir)
	}
}

func TestRun_ScmWorktreeAdd_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-worktree-add-human", map[string]string{
		"worktree_add": `{"protocolVersion":1,"schemaVersion":1,"result":{"path":"/w/feature","branch":"feature","ref":"feature"}}`,
	}, `{}`)
	writeScmConfigFor(t, "backend-worktree-add-human")

	stdout, code := executePr(t, []string{"--output", "human", "scm", "worktree", "add", "feature"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if strings.Contains(stdout, "{") {
		t.Fatalf("human output must not contain raw JSON; stdout=%s", stdout)
	}
	for _, want := range []string{"worktree: /w/feature", "branch: feature", "ref: feature"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

func TestRun_ScmWorktreeRemove_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-worktree-remove-human", map[string]string{
		"worktree_remove": `{"protocolVersion":1,"schemaVersion":1,"result":null}`,
	}, `{}`)
	writeScmConfigFor(t, "backend-worktree-remove-human")

	stdout, code := executePr(t, []string{"--output", "human", "scm", "worktree", "remove", "/w/feature"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "Worktree removed: /w/feature") {
		t.Fatalf("human output = %q", stdout)
	}
}

func TestRun_ScmWorktreeList_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-worktree-list-human", map[string]string{
		"worktree_list": `{"protocolVersion":1,"schemaVersion":1,"result":[{"path":"/w/a","branch":"a","ref":"a"},{"path":"/w/b","branch":"b","ref":"b"}]}`,
	}, `{}`)
	writeScmConfigFor(t, "backend-worktree-list-human")

	stdout, code := executePr(t, []string{"--output", "human", "scm", "worktree", "list"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	for _, want := range []string{"worktrees (2)", "/w/a (branch=a, ref=a)", "/w/b (branch=b, ref=b)"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human output missing %q; stdout=%s", want, stdout)
		}
	}
}

func TestRun_ScmBranchDetect_HumanOutput(t *testing.T) {
	writeOpAwareFakeBackend(t, "backend-branch-detect-human", map[string]string{
		"branch_detect": `{"protocolVersion":1,"schemaVersion":1,"result":{"repo":"owner/repo","branch":"feature"}}`,
	}, `{}`)
	writeScmConfigFor(t, "backend-branch-detect-human")

	stdout, code := executePr(t, []string{"--output", "human", "scm", "branch", "detect", "/home/u/repo"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%s", code, stdout)
	}
	if !strings.Contains(stdout, "repo: owner/repo") || !strings.Contains(stdout, "branch: feature") {
		t.Fatalf("human output = %q", stdout)
	}
}

func TestRun_ScmWorktreeAdd_NoBackendRegistered_IsGenericFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := dir + "/config.yaml"
	if err := os.WriteFile(cfg, []byte("connector: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PG_PR_CONFIG", cfg)

	_, code := executePr(t, []string{"scm", "worktree", "add", "feature"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

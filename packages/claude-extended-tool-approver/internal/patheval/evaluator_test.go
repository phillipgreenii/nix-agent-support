package patheval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathEvaluator_ProjectPath_ReadWrite(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	if got := pe.Evaluate("/project/foo.go"); got != PathReadWrite {
		t.Errorf("Evaluate(/project/foo.go) = %v, want PathReadWrite", got)
	}
	if got := pe.Evaluate("/project/subdir/bar.go"); got != PathReadWrite {
		t.Errorf("Evaluate(/project/subdir/bar.go) = %v, want PathReadWrite", got)
	}
}

func TestPathEvaluator_Tmp_ReadWrite(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	if got := pe.Evaluate("/tmp/foo"); got != PathReadWrite {
		t.Errorf("Evaluate(/tmp/foo) = %v, want PathReadWrite", got)
	}
	if got := pe.Evaluate("/tmp"); got != PathReadWrite {
		t.Errorf("Evaluate(/tmp) = %v, want PathReadWrite", got)
	}
}

func TestPathEvaluator_NixStore_ReadOnly(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	if got := pe.Evaluate("/nix/store/abc123"); got != PathReadOnly {
		t.Errorf("Evaluate(/nix/store/abc123) = %v, want PathReadOnly", got)
	}
}

func TestPathEvaluator_NixStoreRoot_ReadOnly(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	if got := pe.Evaluate("/nix/store"); got != PathReadOnly {
		t.Errorf("Evaluate(/nix/store) = %v, want PathReadOnly", got)
	}
}

func TestPathEvaluator_ClaudePlugins_ReadOnly(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp"
	}
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".claude/plugins/x")
	if got := pe.Evaluate(path); got != PathReadOnly {
		t.Errorf("Evaluate(%s) = %v, want PathReadOnly", path, got)
	}
}

func TestPathEvaluator_ClaudePlans_ReadWrite(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp"
	}
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".claude/plans/some-plan.md")
	if got := pe.Evaluate(path); got != PathReadWrite {
		t.Errorf("Evaluate(%s) = %v, want PathReadWrite", path, got)
	}
}

func TestPathEvaluator_ClaudeProjects_ReadWrite(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp"
	}
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".claude/projects/myproject/memory/notes.md")
	if got := pe.Evaluate(path); got != PathReadWrite {
		t.Errorf("Evaluate(%s) = %v, want PathReadWrite", path, got)
	}
}

func TestPathEvaluator_ClaudeSettings_ReadOnly(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp"
	}
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".claude/settings.json")
	if got := pe.Evaluate(path); got != PathReadOnly {
		t.Errorf("Evaluate(%s) = %v, want PathReadOnly (not writable)", path, got)
	}
}

func TestPathEvaluator_GoPkg_ReadOnly(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/tmp"
	}
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, "go/pkg/mod/foo")
	if got := pe.Evaluate(path); got != PathReadOnly {
		t.Errorf("Evaluate(%s) = %v, want PathReadOnly", path, got)
	}
}

func TestPathEvaluator_Etc_Unknown(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	if got := pe.Evaluate("/etc/passwd"); got != PathUnknown {
		t.Errorf("Evaluate(/etc/passwd) = %v, want PathUnknown", got)
	}
}

func TestPathEvaluator_Usr_Unknown(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	if got := pe.Evaluate("/usr/bin/ls"); got != PathUnknown {
		t.Errorf("Evaluate(/usr/bin/ls) = %v, want PathUnknown", got)
	}
}

func TestPathEvaluator_RelativePathResolved(t *testing.T) {
	pe := NewWithCWD("/project", "/project/src")
	if got := pe.Evaluate("foo.go"); got != PathReadWrite {
		t.Errorf("Evaluate(foo.go) from cwd /project/src = %v, want PathReadWrite", got)
	}
}

func TestPathEvaluator_TildeExpansion(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	pe := NewWithCWD("/project", "/project")
	path := "~/.claude/plugins/x"
	if got := pe.Evaluate(path); got != PathReadOnly {
		t.Errorf("Evaluate(%s) = %v, want PathReadOnly", path, got)
	}
}

func TestPathEvaluator_ProjectRoot(t *testing.T) {
	pe := New("/project")
	if got := pe.ProjectRoot(); got != "/project" {
		t.Errorf("ProjectRoot() = %q, want /project", got)
	}
}

func TestDetectProjectRoot_MONOREPO_ROOT_CWDInside(t *testing.T) {
	t.Setenv("MONOREPO_ROOT", "/mono/root")
	t.Setenv("ZR_MONOREPO", "")
	if got := DetectProjectRoot("/mono/root/subdir"); got != "/mono/root" {
		t.Errorf("DetectProjectRoot with MONOREPO_ROOT (cwd inside) = %q, want /mono/root", got)
	}
}

func TestDetectProjectRoot_MONOREPO_ROOT_CWDIsRoot(t *testing.T) {
	t.Setenv("MONOREPO_ROOT", "/mono/root")
	t.Setenv("ZR_MONOREPO", "")
	if got := DetectProjectRoot("/mono/root"); got != "/mono/root" {
		t.Errorf("DetectProjectRoot with MONOREPO_ROOT (cwd is root) = %q, want /mono/root", got)
	}
}

func TestDetectProjectRoot_MONOREPO_ROOT_CWDOutside(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MONOREPO_ROOT", "/mono/root")
	t.Setenv("ZR_MONOREPO", "")
	// CWD is outside MONOREPO_ROOT and has no .git, so should fall back to cwd
	got := DetectProjectRoot(tmp)
	if got == "/mono/root" {
		t.Errorf("DetectProjectRoot should NOT return env var root when cwd is outside it, got %q", got)
	}
	if got != tmp {
		t.Errorf("DetectProjectRoot = %q, want %q (fallback to cwd)", got, tmp)
	}
}

func TestDetectProjectRoot_LegacyZR_MONOREPO_CWDInside(t *testing.T) {
	t.Setenv("MONOREPO_ROOT", "")
	t.Setenv("ZR_MONOREPO", "/legacy/root")
	if got := DetectProjectRoot("/legacy/root/pkg/foo"); got != "/legacy/root" {
		t.Errorf("DetectProjectRoot with ZR_MONOREPO (cwd inside) = %q, want /legacy/root", got)
	}
}

func TestDetectProjectRoot_LegacyZR_MONOREPO_CWDOutside(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MONOREPO_ROOT", "")
	t.Setenv("ZR_MONOREPO", "/legacy/root")
	got := DetectProjectRoot(tmp)
	if got == "/legacy/root" {
		t.Errorf("DetectProjectRoot should NOT return env var root when cwd is outside it, got %q", got)
	}
}

func TestDetectProjectRoot_MONOREPO_ROOT_TakesPrecedence(t *testing.T) {
	t.Setenv("MONOREPO_ROOT", "/new/root")
	t.Setenv("ZR_MONOREPO", "/old/root")
	if got := DetectProjectRoot("/new/root/subdir"); got != "/new/root" {
		t.Errorf("DetectProjectRoot MONOREPO_ROOT should take precedence, got %q, want /new/root", got)
	}
}

func TestDetectProjectRoot_EnvVarIgnoredFallsToGitWalk(t *testing.T) {
	// Create a temp dir with .git to simulate a non-monorepo project
	tmp := t.TempDir()
	subdir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Setenv("ZR_MONOREPO", "/mono/repo")
	t.Setenv("MONOREPO_ROOT", "")
	// CWD is inside the temp git repo, outside ZR_MONOREPO — should find .git
	got := DetectProjectRoot(subdir)
	if got != tmp {
		t.Errorf("DetectProjectRoot = %q, want %q (git walk should find .git)", got, tmp)
	}
}

func TestDetectProjectRoot_WalksUpForGit(t *testing.T) {
	tmp := t.TempDir()
	gitDir := filepath.Join(tmp, "a", "b", "c")
	if err := os.MkdirAll(filepath.Join(gitDir, ".git"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Setenv("MONOREPO_ROOT", "")
	t.Setenv("ZR_MONOREPO", "")
	got := DetectProjectRoot(gitDir)
	if got != gitDir {
		t.Errorf("DetectProjectRoot(%s) = %q, want %q", gitDir, got, gitDir)
	}
}

func TestDetectProjectRoot_FallbackToCwd(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MONOREPO_ROOT", "")
	t.Setenv("ZR_MONOREPO", "")
	got := DetectProjectRoot(tmp)
	if got != tmp {
		t.Errorf("DetectProjectRoot(%s) = %q, want %q (fallback to cwd)", tmp, got, tmp)
	}
}

func TestPathEvaluator_NixSupportLocalPlugins_ReadOnly(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".local/share/nix-support-local-plugins/plugins/claude-extended-tool-approver/hooks.json")
	if got := pe.Evaluate(path); got != PathReadOnly {
		t.Errorf("Evaluate(%s) = %v, want PathReadOnly", path, got)
	}
}

func TestPathEvaluator_NixSupportLocalPluginsRoot_ReadOnly(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".local/share/nix-support-local-plugins")
	if got := pe.Evaluate(path); got != PathReadOnly {
		t.Errorf("Evaluate(%s) = %v, want PathReadOnly", path, got)
	}
}

func TestPathEvaluator_XDGDataHome_NixSupportLocalPlugins(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")
	pe := NewWithCWD("/project", "/project")
	path := "/custom/data/nix-support-local-plugins/plugins/foo"
	if got := pe.Evaluate(path); got != PathReadOnly {
		t.Errorf("Evaluate(%s) with XDG_DATA_HOME = %v, want PathReadOnly", path, got)
	}
}

func TestEvaluator_WorkspaceRoot(t *testing.T) {
	t.Setenv("WORKSPACE_ROOT", "/Users/testuser/workspace")
	pe := New("/Users/testuser/workspace/repo-a")

	tests := []struct {
		path string
		want PathAccess
	}{
		{"/Users/testuser/workspace/repo-a/file.go", PathReadWrite},
		{"/Users/testuser/workspace/repo-b/file.go", PathReadWrite},
		{"/Users/testuser/workspace/.worktrees/x/file.go", PathReadWrite},
		{"/Users/testuser/other/file.go", PathUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := pe.Evaluate(tt.path)
			if got != tt.want {
				t.Errorf("Evaluate(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestPathEvaluator_EnvVarExpansion(t *testing.T) {
	orig := os.Getenv("HOME")
	defer os.Setenv("HOME", orig)
	os.Setenv("HOME", "/home/testuser")

	pe := New("/home/testuser/project")

	tests := []struct {
		name string
		path string
		want PathAccess
	}{
		{"$HOME in project", "$HOME/project/file.txt", PathReadWrite},
		{"${HOME} in project", "${HOME}/project/file.txt", PathReadWrite},
		{"$HOME to tmp", "$HOME/../../../tmp/file.txt", PathReadWrite},
		{"unset var", "$UNDEFINED_VAR_12345/file.txt", PathUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pe.Evaluate(tt.path)
			if got != tt.want {
				t.Errorf("Evaluate(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestPathEvaluator_SymlinkResolution(t *testing.T) {
	projectDir := t.TempDir()
	outsideDir := t.TempDir()

	symlinkPath := filepath.Join(projectDir, "escape-link")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	pe := New(projectDir)

	got := pe.Evaluate(symlinkPath + "/secret.txt")
	if got != PathUnknown {
		t.Errorf("Evaluate(symlink escaping project) = %v, want PathUnknown", got)
	}

	realFile := filepath.Join(projectDir, "real.txt")
	os.WriteFile(realFile, []byte("ok"), 0644)
	got = pe.Evaluate(realFile)
	if got != PathReadWrite {
		t.Errorf("Evaluate(real project file) = %v, want PathReadWrite", got)
	}
}

func TestPathEvaluator_NonExistentFileInExistingDir(t *testing.T) {
	projectDir := t.TempDir()
	pe := New(projectDir)

	newFile := filepath.Join(projectDir, "new-file.txt")
	got := pe.Evaluate(newFile)
	if got != PathReadWrite {
		t.Errorf("Evaluate(non-existent file in project dir) = %v, want PathReadWrite", got)
	}
}

func TestPathEvaluator_BrokenSymlink(t *testing.T) {
	projectDir := t.TempDir()
	brokenLink := filepath.Join(projectDir, "broken")
	os.Symlink("/nonexistent/target", brokenLink)

	pe := New(projectDir)
	got := pe.Evaluate(brokenLink)
	if got != PathUnknown {
		t.Errorf("Evaluate(broken symlink) = %v, want PathUnknown", got)
	}
}

func TestEvaluator_GradleCache(t *testing.T) {
	pe := New("/tmp/project")
	t.Run("default gradle home", func(t *testing.T) {
		home, _ := os.UserHomeDir()
		got := pe.Evaluate(filepath.Join(home, ".gradle", "caches", "modules-2", "files-2.1", "some.jar"))
		if got != PathReadOnly {
			t.Errorf("got %v, want PathReadOnly", got)
		}
	})
	t.Run("custom gradle home", func(t *testing.T) {
		t.Setenv("GRADLE_USER_HOME", "/custom/gradle")
		pe2 := New("/tmp/project")
		got := pe2.Evaluate("/custom/gradle/caches/modules-2/files-2.1/some.jar")
		if got != PathReadOnly {
			t.Errorf("got %v, want PathReadOnly", got)
		}
	})
}

func TestPathEvaluator_NilConfig(t *testing.T) {
	projectDir := t.TempDir()
	pe := New(projectDir)
	// No SetConfig called — should work exactly as before
	if got := pe.Evaluate(filepath.Join(projectDir, "foo")); got != PathReadWrite {
		t.Errorf("Evaluate(project file) = %v, want PathReadWrite", got)
	}
	if got := pe.Evaluate("/random/path"); got != PathUnknown {
		t.Errorf("Evaluate(random path) = %v, want PathUnknown", got)
	}
}

func TestPathAccess_CanRead(t *testing.T) {
	tests := []struct {
		access PathAccess
		want   bool
	}{
		{PathReject, false},
		{PathUnknown, false},
		{PathReadOnly, true},
		{PathReadWrite, true},
	}
	for _, tt := range tests {
		if got := tt.access.CanRead(); got != tt.want {
			t.Errorf("%v.CanRead() = %v, want %v", tt.access, got, tt.want)
		}
	}
}

func TestPathAccess_CanWrite(t *testing.T) {
	tests := []struct {
		access PathAccess
		want   bool
	}{
		{PathReject, false},
		{PathUnknown, false},
		{PathReadOnly, false},
		{PathReadWrite, true},
	}
	for _, tt := range tests {
		if got := tt.access.CanWrite(); got != tt.want {
			t.Errorf("%v.CanWrite() = %v, want %v", tt.access, got, tt.want)
		}
	}
}

func TestPathEvaluator_IsDenyRead_ConfiguredPath(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		DenyRead: []string{"/Users/phillipg/.ssh"},
	})
	if !pe.IsDenyRead("/Users/phillipg/.ssh/id_rsa") {
		t.Error("IsDenyRead(/Users/phillipg/.ssh/id_rsa) = false, want true")
	}
	if !pe.IsDenyRead("/Users/phillipg/.ssh") {
		t.Error("IsDenyRead(/Users/phillipg/.ssh) = false, want true")
	}
}

func TestPathEvaluator_IsDenyRead_AllowReadOverrides(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		DenyRead:  []string{"/Users/phillipg"},
		AllowRead: []string{"/Users/phillipg/phillipg_mbp"},
	})
	if pe.IsDenyRead("/Users/phillipg/phillipg_mbp/foo.go") {
		t.Error("IsDenyRead in allowRead region should be false (allowRead takes precedence)")
	}
	if !pe.IsDenyRead("/Users/phillipg/Documents/secret.txt") {
		t.Error("IsDenyRead outside allowRead region should be true")
	}
}

func TestPathEvaluator_IsDenyRead_UnconfiguredPath(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		DenyRead: []string{"/Users/phillipg/.ssh"},
	})
	if pe.IsDenyRead("/Users/phillipg/phillipg_mbp/foo.go") {
		t.Error("IsDenyRead for unconfigured path = true, want false")
	}
}

func TestPathEvaluator_IsDenyRead_NilConfig(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	// no SetSandboxConfig call — sandboxConfig is nil
	if pe.IsDenyRead("/Users/phillipg/.ssh/id_rsa") {
		t.Error("IsDenyRead with nil sandboxConfig = true, want false")
	}
}

func TestPathEvaluator_IsDenyWrite_ConfiguredPath(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		DenyWrite: []string{"/Users/phillipg/.ssh"},
	})
	if !pe.IsDenyWrite("/Users/phillipg/.ssh/id_rsa") {
		t.Error("IsDenyWrite(/Users/phillipg/.ssh/id_rsa) = false, want true")
	}
}

func TestPathEvaluator_IsDenyWrite_CWDNotExempt(t *testing.T) {
	// denyWrite takes priority over CWD default — even project files can be protected
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		DenyWrite: []string{"/project/secrets"},
	})
	if !pe.IsDenyWrite("/project/secrets/key.pem") {
		t.Error("IsDenyWrite for denyWrite path under CWD = false, want true")
	}
}

func TestPathEvaluator_IsDenyWrite_NilConfig(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	if pe.IsDenyWrite("/Users/phillipg/.ssh/id_rsa") {
		t.Error("IsDenyWrite with nil sandboxConfig = true, want false")
	}
}

func TestPathEvaluator_IsDenyWrite_OverridesAllowWrite(t *testing.T) {
	// denyWrite takes highest priority — even allowWrite paths are blocked
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		AllowWrite: []string{"/project/secrets"},
		DenyWrite:  []string{"/project/secrets"},
	})
	if !pe.IsDenyWrite("/project/secrets/key.pem") {
		t.Error("IsDenyWrite = false, want true: denyWrite must take precedence over allowWrite")
	}
}

func TestPathEvaluator_ExtToolApprover_ReadWrite(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".local/share/claude-extended-tool-approver/asks.db")
	if got := pe.Evaluate(path); got != PathReadWrite {
		t.Errorf("Evaluate(%s) = %v, want PathReadWrite", path, got)
	}
}

func TestPathEvaluator_ExtToolApprover_XDGDataHome_ReadWrite(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")
	pe := NewWithCWD("/project", "/project")
	path := "/custom/data/claude-extended-tool-approver/asks.db"
	if got := pe.Evaluate(path); got != PathReadWrite {
		t.Errorf("Evaluate(%s) with XDG_DATA_HOME = %v, want PathReadWrite", path, got)
	}
}

func TestPathEvaluator_AllowWrite_IsReadWrite(t *testing.T) {
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		AllowWrite: []string{"/Users/phillipg/.local/share/contained-claude"},
	})
	if got := pe.Evaluate("/Users/phillipg/.local/share/contained-claude/foo"); got != PathReadWrite {
		t.Errorf("Evaluate for allowWrite path = %v, want PathReadWrite", got)
	}
}

package patheval

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

// pinnedHome is the fixed HOME every home-relative zone test runs against.
//
// These tests used to read the AMBIENT HOME (with a "/tmp" fallback or a
// t.Skip), which made them non-hermetic in two ways at once. Under the nix
// build sandbox HOME is $TMPDIR, which on darwin resolves under /nix/var/nix/
// builds/... — so the /nix rule in Evaluate fired first and:
//   - the PathReadWrite assertions FAILED (a real, reproducible break), and
//   - the PathReadOnly assertions PASSED VACUOUSLY — satisfied by the /nix
//     rule rather than by the home-relative rule actually under test.
//
// Pinning HOME makes both kinds meaningful and makes the whole file independent
// of the environment it runs in. The path need not exist: every home-relative
// zone check in Evaluate is a string comparison against pe.home, and
// resolveRefPath falls back to the cleaned path for a non-existent one.
const pinnedHome = "/home/testuser"

// withPinnedHome pins HOME to pinnedHome for the duration of t and returns it.
// It MUST be called BEFORE the evaluator is constructed: New/NewWithCWD read
// the environment once, at construction time.
//
// It also CLEARS XDG_DATA_HOME, which is load-bearing rather than tidiness:
// New/NewWithCWD only derive xdgDataHome as <home>/.local/share when
// XDG_DATA_HOME is EMPTY, otherwise they take it verbatim. A developer shell
// commonly exports XDG_DATA_HOME (e.g. /Users/<user>/.local/share), so pinning
// HOME alone would DESYNCHRONISE the two and the tests asserting on the default
// <home>/.local/share location would evaluate to PathUnknown. Clearing it is
// also the accurate expression of their intent — the XDG_DATA_HOME OVERRIDE has
// its own dedicated tests (…_XDGDataHome_…), which set it explicitly.
func withPinnedHome(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", pinnedHome)
	t.Setenv("XDG_DATA_HOME", "")
	return pinnedHome
}

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
	home := withPinnedHome(t)
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".claude/plugins/x")
	if got := pe.Evaluate(path); got != PathReadOnly {
		t.Errorf("Evaluate(%s) = %v, want PathReadOnly", path, got)
	}
}

func TestPathEvaluator_ClaudePlans_ReadWrite(t *testing.T) {
	home := withPinnedHome(t)
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".claude/plans/some-plan.md")
	if got := pe.Evaluate(path); got != PathReadWrite {
		t.Errorf("Evaluate(%s) = %v, want PathReadWrite", path, got)
	}
}

func TestPathEvaluator_ClaudeProjects_ReadWrite(t *testing.T) {
	home := withPinnedHome(t)
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".claude/projects/myproject/memory/notes.md")
	if got := pe.Evaluate(path); got != PathReadWrite {
		t.Errorf("Evaluate(%s) = %v, want PathReadWrite", path, got)
	}
}

func TestPathEvaluator_ClaudeSettings_ReadOnly(t *testing.T) {
	home := withPinnedHome(t)
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".claude/settings.json")
	if got := pe.Evaluate(path); got != PathReadOnly {
		t.Errorf("Evaluate(%s) = %v, want PathReadOnly (not writable)", path, got)
	}
}

func TestPathEvaluator_GoPkg_ReadOnly(t *testing.T) {
	home := withPinnedHome(t)
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
	// The evaluator expands "~" from HOME itself, so pinning HOME is what makes
	// this assertion test the .claude rule rather than the /nix rule.
	withPinnedHome(t)
	pe := NewWithCWD("/project", "/project")
	path := "~/.claude/plugins/x"
	if got := pe.Evaluate(path); got != PathReadOnly {
		t.Errorf("Evaluate(%s) = %v, want PathReadOnly", path, got)
	}
}

// TestPathEvaluator_TildeUserExpansion is the tc-fielf regression on cleanPath.
// A "~user" argument (tilde + username, no leading slash) MUST resolve via an
// os/user lookup to that user's real home — NOT via pe.home, which produced a
// garbage path like /home/testuseruser (pe.home + "user") and could land in a
// writable zone. An UNKNOWN user MUST NOT resolve to a garbage/writable path.
func TestPathEvaluator_TildeUserExpansion(t *testing.T) {
	// Pin HOME so a bug that mistakenly used pe.home for "~root" would produce a
	// value clearly distinct from root's real home, making the assertion sharp.
	withPinnedHome(t)
	pe := NewWithCWD("/project", "/project")

	// A real user ("root" is guaranteed on Linux/darwin) resolves to that user's
	// actual home. Skip if the sandbox has no user database entry for it.
	u, err := user.Lookup("root")
	if err != nil {
		t.Skip("user.Lookup(root) failed in this sandbox; cannot exercise ~user expansion")
	}
	wantHome := filepath.Clean(u.HomeDir)
	if got := pe.CleanPath("~root"); got != wantHome {
		t.Errorf("CleanPath(~root) = %q, want %q (real home, not pe.home-derived)", got, wantHome)
	}
	// The trailing remainder after ~user is appended to the resolved home.
	if got := pe.CleanPath("~root/sub/file"); got != filepath.Clean(u.HomeDir+"/sub/file") {
		t.Errorf("CleanPath(~root/sub/file) = %q, want %q", got, filepath.Clean(u.HomeDir+"/sub/file"))
	}
	// It must NOT be the pe.home-derived garbage the old code produced.
	if got := pe.CleanPath("~root"); got == filepath.Clean(pinnedHome+"root") {
		t.Errorf("CleanPath(~root) = %q resolved via pe.home (garbage), want real home", got)
	}

	// An UNKNOWN user does NOT resolve to a garbage/writable path: cleanPath
	// returns "" -> Evaluate is PathUnknown (neither readable nor writable), so a
	// referencing command is never auto-approved.
	const unknown = "~nonexistent_user_xyz_tc_fielf"
	if got := pe.CleanPath(unknown); got != "" {
		t.Errorf("CleanPath(%s) = %q, want \"\" (unknown user must not expand)", unknown, got)
	}
	if got := pe.Evaluate(unknown); got != PathUnknown {
		t.Errorf("Evaluate(%s) = %v, want PathUnknown", unknown, got)
	}
	if pe.Evaluate(unknown).CanWrite() {
		t.Errorf("Evaluate(%s).CanWrite() = true, want false (must not auto-approve a write)", unknown)
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
	if got := DetectProjectRoot("/mono/root/subdir"); got != "/mono/root" {
		t.Errorf("DetectProjectRoot with MONOREPO_ROOT (cwd inside) = %q, want /mono/root", got)
	}
}

func TestDetectProjectRoot_MONOREPO_ROOT_CWDIsRoot(t *testing.T) {
	t.Setenv("MONOREPO_ROOT", "/mono/root")
	if got := DetectProjectRoot("/mono/root"); got != "/mono/root" {
		t.Errorf("DetectProjectRoot with MONOREPO_ROOT (cwd is root) = %q, want /mono/root", got)
	}
}

func TestDetectProjectRoot_MONOREPO_ROOT_CWDOutside(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MONOREPO_ROOT", "/mono/root")
	// CWD is outside MONOREPO_ROOT and has no .git, so should fall back to cwd
	got := DetectProjectRoot(tmp)
	if got == "/mono/root" {
		t.Errorf("DetectProjectRoot should NOT return env var root when cwd is outside it, got %q", got)
	}
	if got != tmp {
		t.Errorf("DetectProjectRoot = %q, want %q (fallback to cwd)", got, tmp)
	}
}

func TestDetectProjectRoot_MONOREPO_ROOT_TakesPrecedence(t *testing.T) {
	t.Setenv("MONOREPO_ROOT", "/new/root")
	if got := DetectProjectRoot("/new/root/subdir"); got != "/new/root" {
		t.Errorf("DetectProjectRoot MONOREPO_ROOT should take precedence, got %q, want /new/root", got)
	}
}

func TestDetectProjectRoot_EnvVarIgnoredFallsToGitWalk(t *testing.T) {
	// Create a temp dir with .git to simulate a non-monorepo project
	tmp := t.TempDir()
	subdir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Setenv("MONOREPO_ROOT", "/mono/repo")
	// CWD is inside the temp git repo, outside MONOREPO_ROOT — should find .git
	got := DetectProjectRoot(subdir)
	if got != tmp {
		t.Errorf("DetectProjectRoot = %q, want %q (git walk should find .git)", got, tmp)
	}
}

func TestDetectProjectRoot_WalksUpForGit(t *testing.T) {
	tmp := t.TempDir()
	gitDir := filepath.Join(tmp, "a", "b", "c")
	if err := os.MkdirAll(filepath.Join(gitDir, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Setenv("MONOREPO_ROOT", "")
	got := DetectProjectRoot(gitDir)
	if got != gitDir {
		t.Errorf("DetectProjectRoot(%s) = %q, want %q", gitDir, got, gitDir)
	}
}

func TestDetectProjectRoot_FallbackToCwd(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MONOREPO_ROOT", "")
	got := DetectProjectRoot(tmp)
	if got != tmp {
		t.Errorf("DetectProjectRoot(%s) = %q, want %q (fallback to cwd)", tmp, got, tmp)
	}
}

func TestPathEvaluator_NixSupportLocalPlugins_ReadOnly(t *testing.T) {
	home := withPinnedHome(t)
	pe := NewWithCWD("/project", "/project")
	path := filepath.Join(home, ".local/share/nix-support-local-plugins/plugins/claude-extended-tool-approver/hooks.json")
	if got := pe.Evaluate(path); got != PathReadOnly {
		t.Errorf("Evaluate(%s) = %v, want PathReadOnly", path, got)
	}
}

func TestPathEvaluator_NixSupportLocalPluginsRoot_ReadOnly(t *testing.T) {
	home := withPinnedHome(t)
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
	home := withPinnedHome(t)

	pe := New(filepath.Join(home, "project"))

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

	// A symlink inside the project that resolves OUTSIDE it must never be
	// treated as WRITABLE — that is the escalation the escape check guards
	// against (see Evaluate: "the concern is only when a symlink could escalate
	// access"). The exact non-writable value is environment-dependent: natively
	// the target (a sibling t.TempDir()) is PathUnknown, but under the nix build
	// sandbox TMPDIR is the build dir beneath /nix/var/nix/builds/..., so the
	// target lands in the /nix read-only zone and the evaluator (correctly, per
	// its documented "escape to a less-permissive zone" rule) returns
	// PathReadOnly. Both are non-writable — assert the invariant, not one value.
	got := pe.Evaluate(symlinkPath + "/secret.txt")
	if got.CanWrite() {
		t.Errorf("Evaluate(symlink escaping project) = %v, want a non-writable zone", got)
	}

	realFile := filepath.Join(projectDir, "real.txt")
	_ = os.WriteFile(realFile, []byte("ok"), 0o644)
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
	_ = os.Symlink("/nonexistent/target", brokenLink)

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
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("UserHomeDir unavailable")
	}
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		DenyRead: []string{filepath.Join(home, ".ssh")},
	})
	if !pe.IsDenyRead(filepath.Join(home, ".ssh", "id_rsa")) {
		t.Errorf("IsDenyRead(%s) = false, want true", filepath.Join(home, ".ssh", "id_rsa"))
	}
	if !pe.IsDenyRead(filepath.Join(home, ".ssh")) {
		t.Errorf("IsDenyRead(%s) = false, want true", filepath.Join(home, ".ssh"))
	}
}

func TestPathEvaluator_IsDenyRead_AllowReadOverrides(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("UserHomeDir unavailable")
	}
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		DenyRead:  []string{home},
		AllowRead: []string{filepath.Join(home, "workspace")},
	})
	if pe.IsDenyRead(filepath.Join(home, "workspace", "foo.go")) {
		t.Errorf("IsDenyRead(%s) in allowRead region should be false (allowRead takes precedence)", filepath.Join(home, "workspace", "foo.go"))
	}
	if !pe.IsDenyRead(filepath.Join(home, "Documents", "secret.txt")) {
		t.Errorf("IsDenyRead(%s) outside allowRead region should be true", filepath.Join(home, "Documents", "secret.txt"))
	}
}

func TestPathEvaluator_IsDenyRead_UnconfiguredPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("UserHomeDir unavailable")
	}
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		DenyRead: []string{filepath.Join(home, ".ssh")},
	})
	if pe.IsDenyRead(filepath.Join(home, "workspace", "foo.go")) {
		t.Errorf("IsDenyRead(%s) for unconfigured path = true, want false", filepath.Join(home, "workspace", "foo.go"))
	}
}

func TestPathEvaluator_IsDenyRead_NilConfig(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("UserHomeDir unavailable")
	}
	pe := NewWithCWD("/project", "/project")
	// no SetSandboxConfig call — sandboxConfig is nil
	if pe.IsDenyRead(filepath.Join(home, ".ssh", "id_rsa")) {
		t.Errorf("IsDenyRead(%s) with nil sandboxConfig = true, want false", filepath.Join(home, ".ssh", "id_rsa"))
	}
}

func TestPathEvaluator_IsDenyWrite_ConfiguredPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("UserHomeDir unavailable")
	}
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		DenyWrite: []string{filepath.Join(home, ".ssh")},
	})
	if !pe.IsDenyWrite(filepath.Join(home, ".ssh", "id_rsa")) {
		t.Errorf("IsDenyWrite(%s) = false, want true", filepath.Join(home, ".ssh", "id_rsa"))
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
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("UserHomeDir unavailable")
	}
	pe := NewWithCWD("/project", "/project")
	if pe.IsDenyWrite(filepath.Join(home, ".ssh", "id_rsa")) {
		t.Errorf("IsDenyWrite(%s) with nil sandboxConfig = true, want false", filepath.Join(home, ".ssh", "id_rsa"))
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
	home := withPinnedHome(t)
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

// The extra-roots tests use synthetic top-level roots that need not exist:
// resolveRefPath falls back to "/" (always present, on macOS and in the nix
// build sandbox alike), and a top-level "/ceta-test-*" path is outside every
// built-in zone. So the extra-root code path is exercised unconditionally —
// no environment-dependent skip, and the completion gate genuinely validates it.

func TestPathEvaluator_ExtraReadWriteRoots(t *testing.T) {
	rwRoot := "/ceta-test-rw-root"
	// Env is read at construction, so set it BEFORE building the evaluator.
	t.Setenv("CETA_EXTRA_READWRITE_ROOTS", rwRoot)
	pe := New("/project")
	path := filepath.Join(rwRoot, "sub", "file.txt")
	got := pe.Evaluate(path)
	if got != PathReadWrite {
		t.Errorf("Evaluate(%s) = %v, want PathReadWrite", path, got)
	}
	if !got.CanRead() {
		t.Errorf("Evaluate(%s).CanRead() = false, want true", path)
	}
	if !got.CanWrite() {
		t.Errorf("Evaluate(%s).CanWrite() = false, want true", path)
	}
}

func TestPathEvaluator_ExtraReadOnlyRoots(t *testing.T) {
	roRoot := "/ceta-test-ro-root"
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", roRoot)
	pe := New("/project")
	path := filepath.Join(roRoot, "sub", "file.txt")
	got := pe.Evaluate(path)
	if got != PathReadOnly {
		t.Errorf("Evaluate(%s) = %v, want PathReadOnly", path, got)
	}
	if !got.CanRead() {
		t.Errorf("Evaluate(%s).CanRead() = false, want true", path)
	}
	if got.CanWrite() {
		t.Errorf("Evaluate(%s).CanWrite() = true, want false", path)
	}
}

func TestPathEvaluator_ExtraRoots_NeitherIsUnknown(t *testing.T) {
	t.Setenv("CETA_EXTRA_READWRITE_ROOTS", "/ceta-test-rw-root")
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", "/ceta-test-ro-root")
	pe := New("/project")
	path := "/ceta-test-neither-root/sub/file.txt"
	if got := pe.Evaluate(path); got != PathUnknown {
		t.Errorf("Evaluate(%s) under no extra root = %v, want PathUnknown", path, got)
	}
}

func TestPathEvaluator_ExtraRoots_MultipleColonSeparated(t *testing.T) {
	rw1 := "/ceta-test-rw-a"
	rw2 := "/ceta-test-rw-b"
	// Colon-separated list with a stray empty element that must be dropped.
	t.Setenv("CETA_EXTRA_READWRITE_ROOTS", rw1+"::"+rw2)
	pe := New("/project")
	for _, r := range []string{rw1, rw2} {
		path := filepath.Join(r, "file.txt")
		if got := pe.Evaluate(path); got != PathReadWrite {
			t.Errorf("Evaluate(%s) = %v, want PathReadWrite", path, got)
		}
	}
}

func TestPathEvaluator_AllowWrite_IsReadWrite(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("UserHomeDir unavailable")
	}
	pe := NewWithCWD("/project", "/project")
	pe.SetSandboxConfig(&SandboxFilesystemConfig{
		AllowWrite: []string{filepath.Join(home, ".local", "share", "contained-claude")},
	})
	if got := pe.Evaluate(filepath.Join(home, ".local", "share", "contained-claude", "foo")); got != PathReadWrite {
		t.Errorf("Evaluate(%s) for allowWrite path = %v, want PathReadWrite", filepath.Join(home, ".local", "share", "contained-claude", "foo"), got)
	}
}

// InGitRepo is the PREDICATE half of DetectProjectRoot's `.git` walk, added for
// the `secrets` rule's in-repo relaxation (pg2-pmk9q). The two are not
// interchangeable and this table pins why: DetectProjectRoot FALLS BACK to its
// argument, so its return value cannot distinguish "this is a repo root" from
// "nothing found, here is your cwd back", while a caller that relaxes a security
// control on the answer needs the distinction to be exact.
func TestInGitRepo(t *testing.T) {
	root := t.TempDir()
	// Repo whose marker is a DIRECTORY (an ordinary clone).
	clone := filepath.Join(root, "clone")
	deep := filepath.Join(clone, "a", "b", "c")
	if err := os.MkdirAll(filepath.Join(clone, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	// Repo whose marker is a FILE (a git worktree). This must count: agents in
	// this workspace work almost exclusively in `.worktrees/<name>` checkouts, so
	// a dir-only test would report every one of them as unversioned.
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(filepath.Join(wt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+clone+"/.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Not a repo at all.
	plain := filepath.Join(root, "plain", "sub")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"repo root itself", clone, true},
		{"nested dir in a clone", deep, true},
		{"nonexistent path under a clone", filepath.Join(deep, "no", "such", "file.go"), true},
		{"the .git dir itself", filepath.Join(clone, ".git", "objects"), true},
		{"worktree root (.git is a FILE)", wt, true},
		{"nested dir in a worktree", filepath.Join(wt, "sub"), true},
		{"sibling dir that is not a repo", plain, false},
		{"the temp root above both repos", root, false},
		{"empty path", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InGitRepo(tt.path); got != tt.want {
				t.Errorf("InGitRepo(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// InGitRepo deliberately does NOT honour MONOREPO_ROOT, which DetectProjectRoot
// does. That var overrides which root a project is ATTRIBUTED to; it says nothing
// about whether a path is version-controlled, so honouring it would answer "yes,
// in a repo" for a path under no repo at all — and the `secrets` rule relaxes a
// control on that answer.
func TestInGitRepo_IgnoresMONOREPO_ROOT(t *testing.T) {
	plain := t.TempDir()
	t.Setenv("MONOREPO_ROOT", plain)
	if InGitRepo(filepath.Join(plain, "sub", "file.go")) {
		t.Errorf("InGitRepo honoured MONOREPO_ROOT for %s, which holds no .git", plain)
	}
	// DetectProjectRoot DOES honour it — the contrast is the point.
	if got := DetectProjectRoot(filepath.Join(plain, "sub")); got != plain {
		t.Errorf("DetectProjectRoot = %q, want %q (MONOREPO_ROOT still applies there)", got, plain)
	}
}

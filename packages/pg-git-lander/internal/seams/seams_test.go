package seams_test

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams/seamstest"
)

func TestFilteredEnvRemovesOnlyRepoPointingVars(t *testing.T) {
	base := []string{
		"PATH=/bin", "XDG_CONFIG_HOME=/x", "TMPDIR=/t", "GIT_CONFIG_GLOBAL=/g", "GNUPGHOME=/gpg",
	}
	for _, v := range seams.RepoPointingVars {
		base = append(base, v+"=/elsewhere")
	}
	if len(seams.RepoPointingVars) != 9 {
		t.Fatalf("want 9 repo-pointing vars, got %d", len(seams.RepoPointingVars))
	}
	got := seams.FilteredEnv(base).Environ()
	want := []string{"PATH=/bin", "XDG_CONFIG_HOME=/x", "TMPDIR=/t", "GIT_CONFIG_GLOBAL=/g", "GNUPGHOME=/gpg"}
	if !slices.Equal(got, want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
}

func TestProductionGitEnvDropsGitDir(t *testing.T) {
	t.Setenv("GIT_DIR", "/nope")
	t.Setenv("GNUPGHOME", "/gpg")
	env := seams.ProductionGitEnv().Environ()
	if slices.Contains(env, "GIT_DIR=/nope") || !slices.Contains(env, "GNUPGHOME=/gpg") {
		t.Fatalf("unexpected env filtering")
	}
}

func TestFakeClockAdvances(t *testing.T) {
	c := seamstest.NewClock(time.Unix(100, 0))
	start := c.Now()
	c.Advance(90 * time.Second)
	if got := c.Since(start); got != 90*time.Second {
		t.Fatalf("Since = %v", got)
	}
}

func TestSystemClockMonotonic(t *testing.T) {
	c := seams.SystemClock()
	s := c.Now()
	if c.Since(s) < 0 {
		t.Fatal("negative elapsed")
	}
}

func TestFakeLiveness(t *testing.T) {
	l := seamstest.NewLiveness()
	l.SetAlive(7, "tok")
	if !l.Alive(7, "tok") || l.Alive(7, "other") || l.Alive(8, "") {
		t.Fatal("alive matching wrong")
	}
	l.SetDead(7)
	if l.Alive(7, "") {
		t.Fatal("dead pid reported alive")
	}
}

func TestProcLivenessSelfAndStartTime(t *testing.T) {
	l := seams.ProcLiveness()
	pid := os.Getpid()
	st, err := l.StartTime(pid)
	if err != nil || st == "" {
		t.Fatalf("StartTime = %q, %v", st, err)
	}
	if !l.Alive(pid, st) || !l.Alive(pid, "") {
		t.Fatal("own process not alive")
	}
	if l.Alive(pid, st+"x") {
		t.Fatal("mismatched start time accepted (pid reuse not detected)")
	}
	if l.Alive(0, "") || l.Alive(-1, "") {
		t.Fatal("non-positive pid alive")
	}
}

func TestXDGConfigPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got := seams.XDGConfigPaths().Dir(); got != "/xdg/pg-git-lander" {
		t.Fatalf("Dir = %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/h")
	if got := seams.XDGConfigPaths().Dir(); got != "/h/.config/pg-git-lander" {
		t.Fatalf("fallback Dir = %q", got)
	}
	if seamstest.ConfigPaths("/f").Dir() != "/f" {
		t.Fatal("fake paths")
	}
}

func TestHosts(t *testing.T) {
	if strings.TrimSpace(seams.OSHost().Name()) == "" {
		t.Fatal("empty host")
	}
	if seamstest.Host("other").Name() != "other" {
		t.Fatal("fake host")
	}
}

func TestFakeLivenessZombieCountsAsGone(t *testing.T) {
	l := seamstest.NewLiveness()
	l.SetZombie(7, "tok")
	if l.Alive(7, "tok") || l.Alive(7, "") {
		t.Error("a zombie must not be alive")
	}
	if st, err := l.StartTime(7); err != nil || st != "tok" {
		t.Errorf("StartTime = %q, %v", st, err)
	}
	l.SetAlive(7, "tok")
	if !l.Alive(7, "tok") {
		t.Error("SetAlive must clear the zombie mark")
	}
}

package config

import (
	"strings"
	"testing"
	"time"
)

// TestValidate_permissionMode proves this module's own Config.Validate()
// rejects an invalid claude --permission-mode value (docket pg2-oju6w Task
// 5.7). This assertion MOVED here from packages/pg-router/internal/config's
// own (now-deleted) TestValidate_permissionMode: pg-router no longer checks
// PermissionMode against the real enum, so this module — the side that
// actually invokes `ccpool new --permission-mode` — is where the check, and
// its test, now live.
func TestValidate_permissionMode(t *testing.T) {
	valid := []string{"", "default", "acceptEdits", "plan", "auto", "dontAsk", "bypassPermissions"}
	for _, m := range valid {
		c := Default()
		c.PermissionMode = m
		if err := c.Validate(); err != nil {
			t.Errorf("Validate() with PermissionMode=%q = %v, want nil", m, err)
		}
	}
	for _, m := range []string{"bypass", "Plan", "yolo", "skip-permissions"} {
		c := Default()
		c.PermissionMode = m
		if err := c.Validate(); err == nil {
			t.Errorf("Validate() with PermissionMode=%q = nil, want error", m)
		}
	}
}

func TestDefault_validates(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Errorf("Default() must validate cleanly: %v", err)
	}
}

func TestOriginProbe_defaultsAndValidate(t *testing.T) {
	d := Default().OriginProbe
	if d.FailureThreshold != 2 || d.TTL.Seconds() != 60 || d.Timeout.Seconds() != 20 || len(d.Origins) != 0 {
		t.Fatalf("defaults = %+v, want K=2 TTL=60s timeout=20s no origins", d)
	}
	ok := WatchedOrigin{Key: "git.example.test/org/repo", RepoRoot: "/r"}
	good := d
	good.Origins = []WatchedOrigin{ok}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for name, mut := range map[string]func(*OriginProbe){
		"K=0":             func(o *OriginProbe) { o.FailureThreshold = 0 },
		"timeout 0":       func(o *OriginProbe) { o.Timeout = 0 },
		"negative ttl":    func(o *OriginProbe) { o.TTL = -1 },
		"two-part key":    func(o *OriginProbe) { o.Origins = []WatchedOrigin{{Key: "org/repo", RepoRoot: "/r"}} },
		"scheme key":      func(o *OriginProbe) { o.Origins = []WatchedOrigin{{Key: "https://h/o/r", RepoRoot: "/r"}} },
		"traversal key":   func(o *OriginProbe) { o.Origins = []WatchedOrigin{{Key: "h/../r", RepoRoot: "/r"}} },
		"empty repo root": func(o *OriginProbe) { o.Origins = []WatchedOrigin{{Key: "h/o/r"}} },
		"duplicate key":   func(o *OriginProbe) { o.Origins = []WatchedOrigin{ok, ok} },
	} {
		o := d
		mut(&o)
		if err := o.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want error", name)
		}
	}
}

// TestDefault_leaseTTL pins the supervision-lease default (bead pg2-g2u9m,
// INV-CCH-18): 2 minutes, i.e. 12 polls of the default 10s PollInterval.
func TestDefault_leaseTTL(t *testing.T) {
	d := Default()
	if d.LeaseTTL.Minutes() != 2 {
		t.Fatalf("Default().LeaseTTL = %v, want 2m", d.LeaseTTL)
	}
	if d.PollInterval.Seconds() != 10 {
		t.Fatalf("Default().PollInterval = %v, want 10s", d.PollInterval)
	}
}

// TestValidate_leaseTTL: load rejects a TTL below LeaseTTLMinPolls x
// PollInterval (a live handler's lease could lapse after a few missed
// refreshes) and accepts the boundary.
func TestValidate_leaseTTL(t *testing.T) {
	c := Default()
	c.LeaseTTL = LeaseTTLMinPolls * c.PollInterval // exactly the minimum
	if err := c.Validate(); err != nil {
		t.Errorf("TTL == 10 x PollInterval must validate: %v", err)
	}
	c.LeaseTTL = LeaseTTLMinPolls*c.PollInterval - 1
	if err := c.Validate(); err == nil {
		t.Error("TTL just under 10 x PollInterval must be rejected")
	}
	c.LeaseTTL = 0
	if err := c.Validate(); err == nil {
		t.Error("a zero TTL must be rejected")
	}
	c = Default()
	c.PollInterval = 30 * 1e9 // 30s => 10x = 5m > the 2m default
	if err := c.Validate(); err == nil {
		t.Error("a PollInterval whose 10x exceeds the TTL must be rejected")
	}
}

// bead pg2-uyahp, INV-CCH-23: the per-role worktree quiet-window override.
func TestValidateQuietWindowOverride(t *testing.T) {
	c := Default() // poll 10s, window 2m, quiet max 10m
	for _, tc := range []struct {
		name    string
		window  time.Duration
		wantErr bool
	}{
		{"zero is no override", 0, false},
		{"negative rejected", -time.Second, true},
		{"below two polls rejected", 19 * time.Second, true},
		{"exactly two polls accepted", 20 * time.Second, false},
		{"typical review value accepted", 30 * time.Second, false},
		{"longer than the default accepted", 5 * time.Minute, false},
		{"exactly the wait bound accepted", 10 * time.Minute, false},
		{"beyond the wait bound rejected", 10*time.Minute + time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := c.ValidateQuietWindowOverride(tc.window)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateQuietWindowOverride(%v) err = %v, wantErr %v", tc.window, err, tc.wantErr)
			}
		})
	}
}

// The default is unchanged by the override feature.
func TestDefault_quietWindowStaysTwoMinutes(t *testing.T) {
	if got := Default().WorktreeQuietWindow; got != 2*time.Minute {
		t.Errorf("default WorktreeQuietWindow = %v, want 2m", got)
	}
}

// TestMergeAllowedTools (bead pg2-nk6th.5): the role's extra grants follow the
// handler-wide list in order, a grant already present is dropped (first
// occurrence wins), and no extras returns the base verbatim.
func TestMergeAllowedTools(t *testing.T) {
	for _, tc := range []struct {
		name  string
		base  string
		extra []string
		want  string
	}{
		{"no extras returns base verbatim", " Read , Edit ", nil, " Read , Edit "},
		{"empty extras returns base verbatim", "Read,Edit", []string{}, "Read,Edit"},
		{"extras follow base in order", "Read,Edit", []string{"Skill", "Bash(gh pr view:*)"}, "Read,Edit,Skill,Bash(gh pr view:*)"},
		{"extra already in base is dropped", "Read,Edit,Bash(bd:*)", []string{"Bash(bd:*)", "Skill"}, "Read,Edit,Bash(bd:*),Skill"},
		{"duplicate extras collapse to first", "Read", []string{"Skill", "Agent", "Skill"}, "Read,Skill,Agent"},
		{"duplicate within base collapses", "Read,Edit,Read", []string{"Skill"}, "Read,Edit,Skill"},
		{"empty base", "", []string{"Skill"}, "Skill"},
		{"blank extras ignored", "Read", []string{"", "  ", "Skill"}, "Read,Skill"},
		{"extra is trimmed before comparing", "Read", []string{" Read ", " Skill "}, "Read,Skill"},
		{"comma inside parens is not a separator", "Read,Bash(echo a,b:*)", []string{"Bash(echo a,b:*)", "Skill"}, "Read,Bash(echo a,b:*),Skill"},
		{"extra holding several grants is split and deduped", "Read", []string{"Skill,Read,Agent"}, "Read,Skill,Agent"},
		{"narrow gh grants stay distinct from a prefix grant", "Bash(gh pr:*)", []string{"Bash(gh pr view:*)"}, "Bash(gh pr:*),Bash(gh pr view:*)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MergeAllowedTools(tc.base, tc.extra); got != tc.want {
				t.Errorf("MergeAllowedTools(%q, %q) = %q, want %q", tc.base, tc.extra, got, tc.want)
			}
		})
	}
}

// TestMergeAllowedTools_defaultNeverGainsGitPush pins the bead's invariant:
// `git push` is NOT in the handler-wide default list (a role that needs it gets
// it through extraAllowedTools only), and merging a role's extras leaves the
// default untouched for every other role.
func TestMergeAllowedTools_defaultNeverGainsGitPush(t *testing.T) {
	base := Default().AllowedTools
	for _, g := range SplitAllowedTools(base) {
		if strings.Contains(g, "git push") {
			t.Errorf("handler-wide default AllowedTools contains %q; git push MUST be granted per role only", g)
		}
	}
	_ = MergeAllowedTools(base, []string{"Bash(git push:*)"})
	if Default().AllowedTools != base {
		t.Error("merging mutated the default AllowedTools")
	}
}

func TestSplitAllowedTools(t *testing.T) {
	got := SplitAllowedTools(" Read, Bash(a,b:*) ,,Edit ")
	want := []string{"Read", "Bash(a,b:*)", "Edit"}
	if len(got) != len(want) {
		t.Fatalf("SplitAllowedTools = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SplitAllowedTools[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got := SplitAllowedTools(""); len(got) != 0 {
		t.Errorf("SplitAllowedTools(\"\") = %q, want none", got)
	}
}

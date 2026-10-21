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

// bead pg2-zybju, INV-CCH-29: the needs_input stall bound. Default 15m (operator
// ruling), 0 is the kill switch, anything under six polls (one minute at the 10s
// default) or negative is a config error that names the key.
func TestNeedsInputStall_defaultAndValidate(t *testing.T) {
	if got := Default().NeedsInputStall; got != 15*time.Minute {
		t.Fatalf("default NeedsInputStall = %v, want 15m", got)
	}
	c := Default() // poll 10s
	for _, tc := range []struct {
		name    string
		stall   time.Duration
		wantErr bool
	}{
		{"zero disables", 0, false},
		{"negative rejected", -time.Minute, true},
		{"under six polls rejected", 59 * time.Second, true},
		{"exactly six polls accepted", time.Minute, false},
		{"the default accepted", 15 * time.Minute, false},
		{"longer accepted", 2 * time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc := c
			cc.NeedsInputStall = tc.stall
			err := cc.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() with NeedsInputStall=%v err = %v, wantErr %v", tc.stall, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "needsInputStall") {
				t.Errorf("the error must name the key, got %v", err)
			}
			// The per-role override is checked by the same rule.
			if oerr := c.ValidateNeedsInputStall(tc.stall); (oerr != nil) != tc.wantErr {
				t.Errorf("ValidateNeedsInputStall(%v) err = %v, wantErr %v", tc.stall, oerr, tc.wantErr)
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

// bashPermits reports whether an --allowed-tools list grants the Bash command
// line cmd under dontAsk deny-by-default: some `Bash(<prefix>:*)` grant whose
// prefix is the command itself or a leading word-boundary prefix of it. It
// models only that one grant shape (the only one the code-executing toolchain
// verbs use), not claude's full matcher.
func bashPermits(allowed, cmd string) bool {
	for _, g := range SplitAllowedTools(allowed) {
		prefix, ok := strings.CutPrefix(g, "Bash(")
		if !ok {
			continue
		}
		prefix, ok = strings.CutSuffix(prefix, ":*)")
		if !ok {
			continue
		}
		if cmd == prefix || strings.HasPrefix(cmd, prefix+" ") {
			return true
		}
	}
	return false
}

// hookRunnerCmds are the repo hook runner command lines the handler-wide
// default MUST deny (bead pg2-bg9qq): prek and pre-commit run the hook code the
// repo's own config names, attacker-controlled on an untrusted PR checkout
// (agent-support ADR 0035).
var hookRunnerCmds = []string{
	"prek run --files a.go", "prek run", "pre-commit run --files a.go", "pre-commit install",
}

// goToolchainCmds are the code-executing Go toolchain command lines the
// handler-wide default MUST deny (bead pg2-cwn44): each runs code from the
// tree it is pointed at, which for an untrusted PR checkout is
// attacker-controlled (agent-support ADR 0035).
var goToolchainCmds = []string{
	"go test ./...", "go build ./...", "go vet ./...", "go mod tidy",
	"go generate ./...", "gofmt -l .", "gofumpt -l .",
}

// TestDefault_allowedToolsDeniesGoToolchain (bead pg2-cwn44): the handler-wide
// default grants NO Go toolchain command, so a role that declares no go tool
// set (review, feedback, triager) is denied `go test` by the allowlist. The
// positive control proves the helper can see a grant at all, so the denial
// is not vacuous.
func TestDefault_allowedToolsDeniesGoToolchain(t *testing.T) {
	def := Default().AllowedTools
	for _, cmd := range goToolchainCmds {
		if bashPermits(def, cmd) {
			t.Errorf("default AllowedTools permits %q; go toolchain grants belong to the go tool set only", cmd)
		}
	}
	if !bashPermits(def, "git status") {
		t.Fatal("positive control failed: bashPermits cannot see the default git status grant")
	}
}

// TestDefault_allowedToolsDeniesHookRunners (bead pg2-bg9qq): the handler-wide
// default grants NO prek / pre-commit command, so a role that declares no
// hooks tool set is denied them by the allowlist. The positive control proves
// the helper can see a grant at all, so the denial is not vacuous.
func TestDefault_allowedToolsDeniesHookRunners(t *testing.T) {
	for name, def := range map[string]string{
		"Default":          Default().AllowedTools,
		"baseAllowedTools": baseAllowedTools,
	} {
		for _, cmd := range hookRunnerCmds {
			if bashPermits(def, cmd) {
				t.Errorf("%s permits %q; hook runner grants belong to the hooks tool set only", name, cmd)
			}
		}
		if !bashPermits(def, "git status") {
			t.Fatalf("positive control failed for %s: bashPermits cannot see the git status grant", name)
		}
	}
}

// TestMergeAllowedTools_hooksSetGrantsOnlyTheDeclaringRole (bead pg2-bg9qq): a
// role whose extraAllowedTools carry the hooks set (what the nix module
// renders for a role declaring toolSets = [ "hooks" ]) is permitted prek and
// pre-commit, while the default and a role with no extras are not, and the
// hooks set does not drag in a go grant.
func TestMergeAllowedTools_hooksSetGrantsOnlyTheDeclaringRole(t *testing.T) {
	hooksSet := []string{"Bash(prek:*)", "Bash(pre-commit:*)"}
	def := Default().AllowedTools
	declares := MergeAllowedTools(def, hooksSet)
	for _, cmd := range hookRunnerCmds {
		if !bashPermits(declares, cmd) {
			t.Errorf("role declaring the hooks set is denied %q", cmd)
		}
	}
	if bashPermits(declares, "go test ./...") {
		t.Error("hooks set must not grant go test")
	}
	if got := MergeAllowedTools(def, nil); bashPermits(got, "prek run") {
		t.Errorf("role declaring no set is permitted prek: %q", got)
	}
}

// TestMergeAllowedTools_goSetGrantsOnlyTheDeclaringRole (bead pg2-cwn44): a
// role whose extraAllowedTools carry the go set (what the nix module renders
// for a role declaring toolSets = [ "go" ]) is permitted `go test`, while the
// default and a role with no extras are not.
func TestMergeAllowedTools_goSetGrantsOnlyTheDeclaringRole(t *testing.T) {
	goSet := []string{"Bash(go build:*)", "Bash(go test:*)", "Bash(go vet:*)", "Bash(go mod:*)", "Bash(gofmt:*)", "Bash(gofumpt:*)"}
	def := Default().AllowedTools
	declares := MergeAllowedTools(def, goSet)
	for _, cmd := range []string{"go test ./...", "go vet ./...", "gofmt -l .", "gofumpt -l ."} {
		if !bashPermits(declares, cmd) {
			t.Errorf("role declaring the go set is denied %q", cmd)
		}
	}
	if bashPermits(declares, "go generate ./...") {
		t.Error("go set must not grant go generate")
	}
	if got := MergeAllowedTools(def, nil); bashPermits(got, "go test ./...") {
		t.Errorf("role declaring no set is permitted go test: %q", got)
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

// TestValidateEffortOverride (bead pg2-svkob, INV-CCH-31): a role's effort
// override is empty or one of the claude --effort levels, and nothing else.
func TestValidateEffortOverride(t *testing.T) {
	for _, ok := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		if err := ValidateEffortOverride(ok); err != nil {
			t.Errorf("ValidateEffortOverride(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"ultra", "High", " high", "none", "0"} {
		err := ValidateEffortOverride(bad)
		if err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("ValidateEffortOverride(%q) = %v, want an error naming it", bad, err)
		}
	}
}

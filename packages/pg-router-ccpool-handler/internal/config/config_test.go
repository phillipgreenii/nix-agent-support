package config

import "testing"

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

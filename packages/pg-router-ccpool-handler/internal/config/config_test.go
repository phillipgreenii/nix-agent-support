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

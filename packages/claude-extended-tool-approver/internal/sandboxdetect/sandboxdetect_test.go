package sandboxdetect

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSettings(t *testing.T, dir, name, body string) {
	t.Helper()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, name), []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestDetect_NoFiles(t *testing.T) {
	dir := t.TempDir()
	if Detect(dir) {
		t.Errorf("expected false when no settings files exist")
	}
}

func TestDetect_ProjectEnabled(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", `{"sandbox":{"enabled":true}}`)
	if !Detect(dir) {
		t.Errorf("expected true when project settings enable sandbox")
	}
}

func TestDetect_ProjectDisabled(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", `{"sandbox":{"enabled":false}}`)
	if Detect(dir) {
		t.Errorf("expected false when project settings explicitly disable sandbox")
	}
}

func TestDetect_LocalOverridesProject(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", `{"sandbox":{"enabled":true}}`)
	writeSettings(t, dir, "settings.local.json", `{"sandbox":{"enabled":false}}`)
	if Detect(dir) {
		t.Errorf("expected local override (false) to win over project (true)")
	}
}

func TestDetect_LocalEnablesAfterProjectUnset(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", `{"permissions":{"allow":[]}}`)
	writeSettings(t, dir, "settings.local.json", `{"sandbox":{"enabled":true}}`)
	if !Detect(dir) {
		t.Errorf("expected local enabled=true to win when project does not set it")
	}
}

func TestDetect_MalformedSettingsIgnored(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", `{not valid json`)
	if Detect(dir) {
		t.Errorf("expected malformed settings to be treated as no opinion")
	}
}

func TestDetect_UnsetFieldIsNoOpinion(t *testing.T) {
	dir := t.TempDir()
	// Project enables; local has a sandbox object but no enabled field —
	// should not flip the result back to false.
	writeSettings(t, dir, "settings.json", `{"sandbox":{"enabled":true}}`)
	writeSettings(t, dir, "settings.local.json", `{"sandbox":{}}`)
	if !Detect(dir) {
		t.Errorf("expected unset enabled in local to leave project value intact")
	}
}

func TestDetect_EmptyProjectDir(t *testing.T) {
	// Should not panic and should not consult project files.
	_ = Detect("")
}

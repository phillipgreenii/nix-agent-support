// Package trust ensures a cwd is pre-trusted in ~/.claude.json so an automated
// `claude` launch does not stall on the interactive folder-trust prompt. That
// prompt BLOCKS the REPL from starting, so SessionStart never fires and the
// waiter hangs forever — which is why trust is guaranteed before launch rather
// than detected after.
//
// The file is Claude-managed: merge-only, write-only-when-missing, atomic,
// read-back-verify. Claude Code rewrites it on its own schedule and does NOT
// honour ccpool's flock, so this read-merge-rename has an unavoidable
// lost-update window and there is no CAS. A lost write degrades to the
// trust-prompt hang, so the path is kept near-zero-frequency by writing only
// when the key is actually missing; the home-manager activation's pre-trust of
// default_cwd is the primary, non-racy path.
package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// EnsureTrusted sets .projects[cwd].hasTrustDialogAccepted = true in the JSON at
// path, preserving all other content. No-op (no rewrite) if already true.
func EnsureTrusted(path, cwd string) error {
	root := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &root); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	projects, _ := root["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	proj, _ := projects[cwd].(map[string]any)
	if proj == nil {
		proj = map[string]any{}
	}
	if proj["hasTrustDialogAccepted"] == true {
		return nil // already trusted — no rewrite (shrinks the race window)
	}
	proj["hasTrustDialogAccepted"] = true
	projects[cwd] = proj
	root["projects"] = projects

	if err := atomicWriteJSON(path, root); err != nil {
		return err
	}
	// Read-back-verify; retry once on mismatch.
	if !IsTrusted(path, cwd) {
		if err := atomicWriteJSON(path, root); err != nil {
			return err
		}
		if !IsTrusted(path, cwd) {
			return fmt.Errorf("trust write for %q did not stick (concurrent Claude write?)", cwd)
		}
	}
	return nil
}

// IsTrusted reports whether cwd is marked hasTrustDialogAccepted in the JSON at path.
func IsTrusted(path, cwd string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var root map[string]any
	if json.Unmarshal(b, &root) != nil {
		return false
	}
	projects, _ := root["projects"].(map[string]any)
	proj, _ := projects[cwd].(map[string]any)
	return proj["hasTrustDialogAccepted"] == true
}

func atomicWriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude.json.tmp-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

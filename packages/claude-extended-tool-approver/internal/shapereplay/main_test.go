package shapereplay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain keeps the filesystem twin's scratch tree out of the evaluator's
// coarse read-only "/nix/**" zone. The twin builds its empty project under
// os.TempDir(); inside a nix build sandbox $TMPDIR is under /nix/var/nix/builds,
// so the twin's own in-project write is refused and every row looks
// filesystem-dependent. Outside a sandbox $TMPDIR is left exactly as found. The
// replacement is /var/tmp, which is neither /tmp nor /nix (the same choice the
// patheval tests make for the same reason).
func TestMain(m *testing.M) {
	effective := os.TempDir()
	if resolved, err := filepath.EvalSymlinks(effective); err == nil {
		effective = resolved
	}
	if effective == "/nix" || strings.HasPrefix(effective, "/nix/") {
		dir, err := os.MkdirTemp("/var/tmp", "ceta-shapereplay-test-tmp-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "shapereplay TestMain: temp dir %q is under /nix and /var/tmp is unusable: %v\n", effective, err)
			os.Exit(1)
		}
		original, had := os.LookupEnv("TMPDIR")
		_ = os.Setenv("TMPDIR", dir)
		code := m.Run()
		_ = os.RemoveAll(dir)
		if had {
			_ = os.Setenv("TMPDIR", original)
		} else {
			_ = os.Unsetenv("TMPDIR")
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

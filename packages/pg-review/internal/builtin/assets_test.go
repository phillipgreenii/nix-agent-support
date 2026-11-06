package builtin_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/builtin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/config"
)

// The built-in lenses must find their prompt assets in the embedded files: an
// empty configuration validates with no problem about a prompt.
func TestBuiltinLensesFindPromptAssets(t *testing.T) {
	root := t.TempDir()
	env := config.Env{ConfigHome: filepath.Join(root, "cfg"), StateHome: filepath.Join(root, "state"), CacheHome: filepath.Join(root, "cache")}
	eff, err := config.Load(env, builtin.FS())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range eff.Validate() {
		// Provider binaries are not on PATH in a unit test; only prompt
		// problems matter here.
		if !strings.Contains(fmt.Sprint(p), "prompt") {
			continue
		}
		t.Errorf("problem: %v", p)
	}
}

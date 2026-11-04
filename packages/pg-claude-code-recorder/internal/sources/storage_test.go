package sources

import (
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
)

// The query layer routes by field name across every source, so two sources
// that declare the same name MUST declare the same typed storage.
func TestStorageDeclarationsAgreeAcrossSources(t *testing.T) {
	seen := map[string]source.Storage{}
	owner := map[string]string{}
	for _, s := range All() {
		st, ok := s.(source.Stored)
		if !ok {
			continue
		}
		for name, d := range st.Storage() {
			if d.Kind == source.StoreFields {
				continue
			}
			if prev, dup := seen[name]; dup && prev != d {
				t.Errorf("%s declares %s as %s, %s declares it as %s", s.Name(), name, d, owner[name], prev)
			}
			seen[name], owner[name] = d, s.Name()
		}
	}
	got := Storage(All())
	if len(got) != len(seen) || got["cwd"].Column != "cwd_id" || got["message.usage.output_tokens"].Table != "assistant_usage" {
		t.Fatalf("merged storage: %v", got)
	}
}

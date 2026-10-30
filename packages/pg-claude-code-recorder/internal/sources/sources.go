// Package sources lists the sources compiled into the recorder.
package sources

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/claudehistory"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/claudetranscript"
)

// All returns the compiled-in sources.
func All() []source.Source {
	return []source.Source{claudetranscript.New(), claudehistory.New()}
}

// Catalog merges every source's field catalog (name to kind).
func Catalog(srcs []source.Source) map[string]source.Kind {
	out := map[string]source.Kind{
		"source": source.KindKeyword, "type": source.KindKeyword,
		"session": source.KindID, "time": source.KindTime,
	}
	for _, s := range srcs {
		if c, ok := s.(source.Catalogued); ok {
			for k, v := range c.Catalog() {
				out[k] = v
			}
		}
	}
	return out
}

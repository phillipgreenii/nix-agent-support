// Package sources lists the sources compiled into the recorder.
package sources

import (
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/claudecontrolend"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/claudehistory"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/claudestatus"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/claudesubagentmeta"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/claudetoolresult"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/claudetranscript"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/sessioncompaction"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/sessionmode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/source/sessionregistry"
)

// All returns the compiled-in sources.
func All() []source.Source {
	return []source.Source{
		claudetranscript.New(), claudehistory.New(), claudesubagentmeta.New(), claudetoolresult.New(),
		sessionregistry.New(), claudestatus.New(), sessionmode.New(), sessioncompaction.New(),
		claudecontrolend.New(),
	}
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

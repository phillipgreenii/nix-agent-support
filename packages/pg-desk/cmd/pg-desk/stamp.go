package main

import (
	"io"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// runningStamp is the interpretation stamp of this build under cfg: the build
// version plus the digest of the configuration interpret reads
// (docs/behavior/pg-desk/reinterpret.md, "The stamp").
func runningStamp(cfg *config.Config) string { return interpret.Stamp(Version, cfg) }

// stampOption is the pipeline option every command that builds a Pipeline
// passes, so each interpretation row it writes carries the running stamp
// (INV-REINTERPRET-4).
func stampOption(cfg *config.Config) pipeline.Option {
	return pipeline.WithStamp(runningStamp(cfg))
}

// newRunPipeline builds the pipeline `run` hydrates and re-interprets through.
func newRunPipeline(cfg *config.Config, st *store.Store, logOut, rec io.Writer) *pipeline.Pipeline {
	return pipeline.New(cfg, st, stampOption(cfg), pipeline.WithVerbose(runF.verbose), pipeline.WithLogWriter(logOut), pipeline.WithRunRecordWriter(rec))
}

// newChangesPipeline builds the pipeline the `changes` engine hydrates through.
func newChangesPipeline(cfg *config.Config, st *store.Store, logOut io.Writer) *pipeline.Pipeline {
	return pipeline.New(cfg, st, pipeline.WithLogWriter(logOut), stampOption(cfg))
}

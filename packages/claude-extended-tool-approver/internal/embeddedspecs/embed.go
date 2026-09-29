// Package embeddedspecs holds the BUILT-IN command-spec data files (Phase 1
// packet 1.2, tc-o14i5.2.2) compiled into the ceta binary as
// internal/specfmt's (packet 1.1) LayerEmbedded fs.FS. Every data/*.json
// file is one specfmt.Spec (KindCommand) marshalled from one
// cmddesc.DefaultRegistry() entry -- see generate.go's BuildSpecs/WriteSpecs
// for how they are produced, TestGenerate (generate_test.go) and
// cmd/genspecs for the two documented, re-runnable ways to regenerate them,
// and TestRoundTrip (roundtrip_test.go) for the proof that loading this
// embedded layer back through specfmt.Repository reflect.DeepEquals the
// live cmddesc.DefaultRegistry() with 0 diffs.
//
// # Why a new package, not a subdirectory of internal/cmddesc or internal/specfmt
//
// Neither existing package is a natural home: internal/cmddesc knows
// nothing about the wire format (specs reference its interpreters/dialects
// BY NAME only -- see specfmt/doc.go's "Unknown-value rejection" section),
// and internal/specfmt is packet 1.1's OWN package -- this packet
// (tc-o14i5.2.2) is explicitly a "pure consumer" of it (Out of scope:
// "Building the loader/format itself -- that is packet 1.1"), so generated
// DATA produced by this packet does not live inside packet 1.1's own
// directory. A sibling package that imports both, and embeds its own data/
// directory, keeps packet 1.1 untouched while giving a later packet (the
// binary's actual wiring, or packet 1.3's linter) one obvious fs.FS to
// import: specfmt.NewRepository(embeddedspecs.FS, userDir, repoDir).
package embeddedspecs

import "embed"

// FS is the embedded built-in spec layer -- pass it as specfmt.Repository's
// Embedded field.
//
//go:embed data/*.json
var FS embed.FS

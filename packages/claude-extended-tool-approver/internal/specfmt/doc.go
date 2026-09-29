// Package specfmt is the versioned, on-disk SPEC FORMAT plus a three-layer
// Repository loader (Phase 1 — Specs as data, packet 1.1, tc-o14i5.2.1).
//
// # Package-naming choice
//
// "specfmt" was chosen (over e.g. "specloader") because the package owns TWO
// things in one place — the on-disk data shapes (the "format") AND the
// Repository that reads/merges them (the "loader") — and both halves are
// small enough, and tightly enough coupled (the loader only ever produces and
// consumes the format's own types), that splitting them into sibling
// packages would just add an import for no isolation benefit. This mirrors
// how internal/cmddesc itself keeps its schema types (schema.go) and their
// interpreters (interpreter.go) in one package.
//
// # What this packet mints
//
// A v1 on-disk COMMAND spec format (FormatVersion, Spec, CommandSpecV1 and
// its nested types in v1.go) that is a 1:1 field-for-field serialization of
// cmddesc.CommandSchema, with a mandatory citation on every fact (a
// help/man/source-line reference recording where the spec author verified
// that fact — see Citation's doc comment). Conversion between the wire
// format and cmddesc.CommandSchema lives in convert.go (ToSchema/FromSchema).
//
// The wire Spec envelope (Spec.Kind, in v1.go) is deliberately SPEC-KIND
// AGNOSTIC: only Kind == KindCommand is populated by this packet (via the
// Command field), but a later phase can add KindPath/KindTarget alongside it
// without changing the envelope, the Repository, or the merge/override
// machinery below — none of that code branches on Kind except to route to
// the right typed field.
//
// # The Repository (three-layer loader)
//
// Repository (repository.go) reads specs from three fs.FS layers in
// precedence order — embedded built-ins < user-level < repo-level — and
// merges them keyed by (Kind, Name). A later layer's spec for the same key
// wins, but if it does not declare Overrides, the merge records a Conflict
// (see MergedSet.Conflicts) rather than silently accepting the shadowing;
// packet 1.3's linter is what turns a Conflict into a hard failure — this
// loader only needs to SURFACE the information (which name, which layers).
//
// Every layer is an fs.FS, so the embedded built-ins (an embed.FS a later
// packet compiles in), the user-level directory, and the repo-level
// directory are all walked by the exact same code (loadLayer in
// repository.go) — the only per-layer difference is which fs.FS a caller
// hands NewRepository. This keeps the Repository itself free of any
// filesystem-path knowledge for the embedded layer, and free of any Nix or
// home-manager dependency for the other two: DefaultUserDir resolves a
// plain directory under the OS home directory (os.UserHomeDir, never
// $XDG_CONFIG_HOME — see its doc comment), and DefaultRepoDir is a plain
// path.Join. A deployment MAY render the user-level directory's files via
// home-manager, but nothing in this package requires that.
//
// # Unknown-value rejection
//
// Validate (validate.go) rejects — reports as an error, never silently
// drops — any command spec naming an unknown Interpreter, program Dialect,
// VerbFamily, or ImplicitEffect.RemoteFamily. Interpreter and Dialect names
// are checked against cmddesc's own registries (cmddesc.LookupInterpreter,
// cmddesc.LookupDialect) — the authoritative sets already maintained there.
// VerbFamily and RemoteFamily have no such registry in cmddesc (they are
// free-form strings stamped onto Effect.Family), so this package keeps its
// own small, explicitly seeded registry (families.go) mirroring the current
// registry_breadth.go usage ("just", "npm", "devbox", "nix" for VerbFamily;
// "kubectl" for RemoteFamily) — RegisterVerbFamily/RegisterRemoteFamily let
// a later packet widen it the same way cmddesc.RegisterDialect/
// RegisterInterpreter already let callers widen those tables.
package specfmt

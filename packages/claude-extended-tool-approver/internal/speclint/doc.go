// Package speclint is the spec LINTER (Phase 1 — Specs as data, packet 1.3,
// tc-o14i5.2.3): it enforces the structural and citation rules P13/P14
// require of a command spec written in packet 1.1's format
// (internal/specfmt), on top of what specfmt.Validate already enforces at
// load time.
//
// # Division of labor with specfmt.Validate
//
// specfmt.Validate (packet 1.1) already fails closed, UNCONDITIONALLY, on:
// an unsupported format version, an empty spec name, a missing per-fact
// citation (Citation.Source == ""), an unknown Interpreter/Dialect/
// VerbFamily/RemoteFamily. A spec that fails Validate is excluded from
// specfmt.Repository's MergedSet.Commands entirely (specfmt.InvalidSpec) —
// this package does not re-implement any of that; LintInvalid only
// SURFACES those pre-existing rejections as WARN lint Findings (see below)
// so a caller of this package's CLI sees one unified report instead of
// having to separately consult specfmt.MergedSet.Invalid — WARN because the
// packet's own Contract calls this surfacing optional ("MAY... but the
// authoritative enforcement point is packet 1.1's loader, not this
// linter") and Repository.Load has already performed the real enforcement
// by excluding the spec from MergedSet.Commands; see LintInvalid's own doc
// comment for a concrete pre-existing case (bash/sh's "shell-file" dialect)
// this WARN severity is load-bearing for.
//
// This package adds FOUR checks specfmt.Validate does not perform, per the
// packet's Contract:
//
//  1. Citation presence (thinness). A citation whose Source is the
//     "internal/cmddesc/registry.go: cmddesc.DefaultRegistry()[...]"
//     provenance string packet 1.2's generator stamped on all 45 embedded
//     built-ins is "thin" — non-empty (so it already passes
//     specfmt.Validate) but not a real fact-by-fact citation. This check
//     flags a thin citation as HARD unconditionally, for every spec
//     regardless of layer. It used to WARN-only for a spec loaded from
//     specfmt.LayerEmbedded (the builtin bool this package's functions
//     still take — see BUILTIN SCOPING below, which is unrelated and
//     unchanged) as a documented interim allowance while packet 1.2's 45
//     mechanically-marshalled built-ins had not yet been back-filled with
//     real citations; docket tc-o14i5.4's Phase 3 packet 1 (tc-o14i5.4.3)
//     completed that back-fill for all 46 embedded built-ins, so the
//     allowance is resolved and this check is HARD everywhere. A literally
//     EMPTY citation never reaches this check at all — specfmt.Validate
//     already rejects it unconditionally (see LintInvalid).
//
//  2. Danger-shaped-flag role check. A flag whose SPELLING matches one of
//     the P13 danger-shaped patterns (-o, --output*, --exec*, -c,
//     --config*, --command, -e, --*-program, --*-hook*, --receive-pack,
//     --upload-pack, --prune, --mirror, --all, --delete, -f/--force*,
//     -r/-R/--recursive) but whose modeled role carries no effect is
//     flagged HARD.
//
//  3. UnknownFlagInert justification. A command using
//     cmddesc.UnknownFlagInert ("inert" on the wire) MUST cite a real
//     (non-thin) justification via its mandatory "unknownFlag" citation —
//     packet 1.1's format has no SEPARATE "justification" field (this
//     packet's Files section explicitly does not modify specfmt's own
//     files), so this check reuses that already-mandatory citation as the
//     justification slot, requiring it to be non-thin. Flagged HARD.
//
//  4. Overrides-conflict reporting. Every specfmt.Conflict (an undeclared
//     override recorded by specfmt.Repository.Load) is surfaced as a HARD
//     finding — the loader's own doc comment says exactly this is packet
//     1.3's job ("packet 1.3's linter is what turns a Conflict into a hard
//     failure").
//
// # BUILTIN SCOPING — checks 2 and 3 only apply to non-builtin specs
//
// Checks 2 and 3 are checks of AUTHORIAL JUDGMENT: did whoever wrote this
// spec by hand correctly classify a scary-looking flag, and did they
// justify treating unknown flags as harmless? The 45 specs packet 1.2
// generated were not hand-written at the spec-format level at all — they
// are a 1:1 mechanical marshalling of internal/cmddesc/registry.go's
// already-independently-reviewed Go schemas (see embeddedspecs/generate.go).
// Any modeling gap in that Go source (for example gitPushSchema's --prune/
// --all/--exec/--receive-pack/--upload-pack currently carrying no wired
// effect — a gap the design's own P15 names as tracked separately, to be
// fixed "in the same change" R6 makes `git push` Permitted, which is a
// LATER phase's scope, not this packet's) is a cmddesc/registry.go
// correctness question, not a spec-FORMAT question this linter exists to
// re-litigate. Checks 2 and 3 therefore take a builtin bool and are
// SKIPPED entirely when builtin is true; check 1 (now unconditionally HARD
// — see its own doc comment above) and check 4 apply regardless of
// builtin.
//
// A caller determines builtin from which specfmt.Layer a spec was loaded
// from: LayerEmbedded is builtin; LayerUser and LayerRepo (and any spec
// read directly from a bare file path, bypassing the Repository) are not.
// See cmd/claude-extended-tool-approver's "lint" subcommand for how the CLI
// derives this from its own flags.
//
// This is a documented, DELIBERATE scope limitation (per the packet's own
// "document the choice made" latitude for undecided specifics), not a
// silent gap: a future hand-written user-level or repo-level spec, or a
// future skill-generated spec (Phase 3), is fully subject to checks 2 and
// 3 the moment it is NOT loaded from the embedded layer.
//
// # Skill-generated specs (Phase 3 stub)
//
// P13 also forbids UnknownFlagInert unconditionally in "skill-generated"
// specs (no justification accepted at all). Phase 1 has no skill-generated
// specs yet — IsSkillGenerated always returns false, a documented no-op
// stub per the packet's own instruction, wired but never yet reachable.
package speclint

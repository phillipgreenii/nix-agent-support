// Package specdrift implements the "--help drift check" (Phase 3 item 2;
// P13/P14; docket tc-o14i5.4, packet tc-o14i5.4.2): one of the mechanical
// fact-ACCURACY gates the design names — a flake check that diffs each
// embedded command spec's flag set against the PINNED BINARY's actually-
// captured --help output, by hashing the captured text and comparing
// against a committed, generated hash file.
//
// # Two modes, one chicken-and-egg problem
//
// The check has to answer "has --help changed since the spec was written",
// which needs a BASELINE hash to compare against. That baseline is
// authored once, locally/in CI where the real tools are on PATH, via
// Record — mirroring cmd/genspecs's existing "committed, regenerate-when-
// needed generated data" convention (see that command's own doc comment):
// Record is never invoked by "nix flake check" itself, only by whoever
// (re)generates internal/embeddedspecs/data/help-hashes.json ahead of a
// commit. Check is the read-only comparison "nix flake check" actually
// runs, entirely against DATA COMPILED INTO THE BINARY (both the embedded
// spec JSON files AND help-hashes.json — see
// internal/embeddedspecs/embed.go's `//go:embed data/*.json`, which already
// covers help-hashes.json since it matches the same glob) — no filesystem
// argument, no repo checkout required at check time, exactly like the
// sibling claude-extended-tool-approver-spec-lint check.
//
// # Shell-builtin exemption
//
// cd and export are pure bash builtins with no separate on-PATH binary to
// invoke --help against (bash ships no standalone /bin/cd or /bin/export).
// Exempt (this package's own package-level var) names them; both are
// recorded/expected as a JSON null in help-hashes.json rather than a hash
// string, and both Record and Check skip the capture/compare step for them
// entirely — this
// is a documented, PINNED exception (this packet's own Contract/Produces),
// not a generic "binary missing" tolerance: any OTHER of the 46 embedded
// command names missing from PATH is the genuine "binary not on PATH"
// environment-gap failure case (see Check's own doc comment).
//
// # Plugin-instructed tools (pg2-cjfpy.2)
//
// The embedded set also carries the CLIs the agent-support marketplace plugins
// instruct (bgcheck, pg-ccaudit, pn, gh, rg, ...). Each on-PATH tool needs its
// package in the nix check's nativeBuildInputs (flake.nix). Three classes of
// name have no single pinned binary to hash and are exempt (recorded null):
// launchctl (a macOS system tool), man (man-db vs the darwin implementation),
// and every plugin helper script (IsExempt's ScriptSuffix rule: create-packet.sh,
// impl-traces.sh, ... are repo files resolved by relative path, not binaries).
//
// # Per-platform exemption and overlay
//
// help-hashes.json is the SHARED baseline and is the linux (procps-ng)
// one. Two mechanisms keep it usable on darwin without clobbering it:
//
//   - ExemptOn/IsExempt: on darwin, ps and pgrep are skipped entirely
//     (neither captured nor compared; Record writes null for them). The nix
//     check sandbox's pkgs.procps on aarch64-darwin is a BSD-ps stub with no
//     pgrep, while ps.json/pgrep.json model procps-ng, so a darwin hash for
//     them would be meaningless -- and would be wrong for linux. Their
//     shared-baseline hashes are left untouched for linux. An exemption
//     (rather than a per-platform hash) is used for these two because the
//     darwin binary is not the tool the spec describes at all.
//   - help-hashes.<goos>.json overlay (PlatformHashesFile, LoadPlatformHashes,
//     Overlay): a per-GOOS file holding only hashes that differ from the
//     shared baseline for tools that exist on both platforms but print
//     different --help text in the nix sandbox (bash, sh, npm, scp, ssh on
//     darwin). Check layers it over the baseline; a non-linux --record
//     writes only this overlay and never the shared file. linux has no
//     overlay.
//
// # Command-name source: the raw embedded spec files, not specfmt.Repository
//
// CommandNames reads internal/embeddedspecs/data/*.json directly as
// specfmt.Spec (json.Unmarshal), NOT through specfmt.Repository.Load /
// specfmt.Validate. Two of the 46 embedded built-ins (bash, sh) fail
// Validate today (a pre-existing, already-documented dialect gap — see
// internal/speclint/lint.go's LintInvalid doc comment) and would be
// silently EXCLUDED from a Repository's MergedSet.Commands, which would
// make this check quietly skip them. The packet's own Contract/Consumes
// names "internal/embeddedspecs/data/*.json (46 files)" as the set to
// check by default — reading the files directly is what actually delivers
// all 46, including bash/sh.
//
// # Dependency injection for testability
//
// Record and Check both take a HelpCapturer rather than calling
// os/exec directly — the CLI wires CaptureHelp (a real `<name> --help`
// subprocess); a unit test wires a small stub returning fixed text, so
// TestCheckDetectsDrift (specdrift_test.go) can assert Check reports a
// drift finding for a deliberately mismatched hash WITHOUT touching any
// real binary or the network (this repo's own unit-test isolation rule).
package specdrift

package specdrift

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
)

// HelpHashesFile is the name (within an embeddedspecs data directory) of
// the committed, generated aggregate hash file this package reads/writes —
// a SIBLING of the 46 per-command spec JSON files, never a new field on
// CommandSpecV1 (this packet's own Contract/Produces "Hash-recording
// convention (pinned)").
const HelpHashesFile = "help-hashes.json"

// Exempt is the set of embedded-spec command names for which Record/Check
// MUST record/expect `null` rather than perform a live --help
// capture/compare — see doc.go's "Shell-builtin exemption".
var Exempt = map[string]bool{
	"cd":     true,
	"export": true,
	// pg2-cjfpy.2: launchctl is a macOS system tool absent from a linux (and a
	// nix-sandboxed darwin) PATH, and `man` is man-db on linux but a different
	// implementation on darwin, so neither has ONE pinned --help text to hash —
	// the same reason ps/pgrep are exempt per-platform below, here universal.
	"launchctl": true,
	"man":       true,
}

// ScriptSuffix marks a spec whose command is a plugin HELPER SCRIPT resolved
// by repo-relative path (create-packet.sh, impl-traces.sh, ...). Such a script
// is a file in the checkout, not an on-PATH binary, so there is no executable
// to run `--help` against in the check sandbox: every name with this suffix is
// exempt (pg2-cjfpy.2; the engine looks these up by script basename —
// effectgraph/build.go's relative-argv0 path — see cmddesc.pluginScriptSchemas).
const ScriptSuffix = ".sh"

// ExemptOn names, per GOOS, the commands whose on-PATH binary in the nix
// check sandbox is not the one the embedded spec models, so Record/Check
// MUST skip them on that platform (and only there). On darwin,
// nixpkgs' pkgs.procps is a BSD-ps stub with no pgrep, while ps.json and
// pgrep.json model procps-ng -- the shared baseline's ps/pgrep hashes stay
// valid for Linux and are simply not consulted on darwin. See doc.go's
// "Per-platform exemption and overlay".
var ExemptOn = map[string]map[string]bool{
	"darwin": {
		"ps":    true,
		"pgrep": true,
	},
}

// ExternalFlake names the embedded commands whose binary is delivered by a
// flake OTHER than this one (pg2-cjfpy.4): the ZR-only daily-focus /
// local-alert-triage / zr-refactor helper scripts (phillipg-nix-ziprecruiter)
// and `pjira` (phillipg-nix-repo-base). This flake takes no input on either,
// so the nix check sandbox can never put them on PATH, and a recorded hash
// would turn the "binary not on PATH" failure mode into a permanent red
// `claude-extended-tool-approver-spec-help-drift` check. Like Exempt they are
// recorded/expected as JSON null, trading --help drift detection for those
// names (a documented loss, not a tolerance for any other missing binary) for
// a check that stays runnable.
var ExternalFlake = map[string]bool{
	"pjira":             true,
	"df-jira-refs":      true,
	"df-resolve-focus":  true,
	"df-split-blockers": true,
	"df-survey":         true,
	"lat-survey":        true,
	"rc-branch":         true,
	"rc-preflight":      true,
	"rc-probe":          true,
	"rc-sentinel":       true,
	// pg2-maars: the bd-writing class D scripts of the same three plugins.
	"df-close-focus": true,
	"df-deferred":    true,
	"df-pull":        true,
	"df-wire":        true,
	"lat-wire":       true,
	"rc-claim":       true,
	"rc-fp":          true,
	"rc-park":        true,
}

// IsExempt reports whether name is exempt from capture/compare on goos:
// universally (Exempt), because its binary ships from another flake
// (ExternalFlake), or for that platform only (ExemptOn).
func IsExempt(goos, name string) bool {
	return Exempt[name] || ExternalFlake[name] || ExemptOn[goos][name] || strings.HasSuffix(name, ScriptSuffix)
}

// PlatformHashesFile returns the name of the per-GOOS overlay file
// ("help-hashes.<goos>.json") holding only the hashes that differ from the
// shared HelpHashesFile baseline on that platform. The shared baseline is
// the linux one; goos "linux" therefore has no overlay.
func PlatformHashesFile(goos string) string {
	return "help-hashes." + goos + ".json"
}

// HelpCapturer captures the live --help output for one command name,
// returning the raw text this package hashes. CaptureHelp is the real,
// exec.Command-backed implementation the CLI wires; tests substitute a
// stub — see doc.go's "Dependency injection for testability".
type HelpCapturer func(name string) (string, error)

// captureEnv is the FIXED, explicit environment CaptureHelp runs every
// child process under, replacing (not merging with) whatever environment
// the ceta process itself happens to have inherited. This is load-bearing,
// not cosmetic: GNU coreutils' --help output varies with the CALLER's
// environment in ways unrelated to the command's own flag set --
// specifically, a locale category (LANG/LC_ALL) other than "C" appends an
// extra "Report any translation bugs to <url>" line, and TERM being unset
// entirely (vs. any non-"dumb" terminal type) suppresses the OSC-8
// hyperlink escapes coreutils 9.x wraps flag names in -- verified
// empirically (tc-o14i5.4.2): capturing from an interactive shell
// (LANG=en_US.UTF-8, TERM=tmux-256color) hashed differently than the exact
// same binary captured inside `nix build`'s sandbox (no LANG, TERM=xterm-
// 256color), even though both are the identical coreutils store path. A
// hash-based drift check is meaningless if the hash itself depends on
// which terminal/locale happened to invoke it, so both --record and
// --check MUST run every capture under this same fixed environment,
// regardless of ceta's own ambient one. HOME is pinned for the same
// reason: `npm --help` prints the resolved path to $HOME/.npmrc inline,
// so a real, machine-specific $HOME leaks into the hashed text — pinned
// to "/homeless-shelter", nix's own standard placeholder for "a HOME no
// real program should successfully use" (`nix build`'s sandbox already
// sets exactly this, which is how this was caught: capturing with HOME
// unset entirely, rather than pinned, produced a THIRD distinct hash from
// either the real-$HOME or the sandbox's own capture).
var captureEnv = []string{
	"TERM=xterm-256color",
	"LC_ALL=C",
	"LANG=C",
	"HOME=/homeless-shelter",
}

// CaptureHelp runs `name --help` (under captureEnv -- see its doc comment)
// and returns its combined stdout+stderr text. A nonzero exit is NOT
// itself an error here — many commands exit nonzero for --help (or print
// it to stderr); what this function reports as an error is the command
// failing to even START (e.g. "binary not on PATH"), which is the
// caller's (Check's) "binary not on PATH" failure case. Exempt commands
// (cd, export) must never reach this function — callers check Exempt
// first.
func CaptureHelp(name string) (string, error) {
	cmd := exec.Command(name, "--help")
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, captureEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// The binary ran and exited nonzero -- its (possibly error-
			// shaped) output is still the live text this check hashes.
			return string(out), nil
		}
		return "", fmt.Errorf("specdrift: running %q --help: %w", name, err)
	}
	return string(out), nil
}

// Hash returns the hex-encoded SHA-256 of text — the hash algorithm this
// packet's Contract/Produces pins.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// CommandNames returns the sorted list of command names found in fsys's
// embedded spec JSON files under dataDir (e.g. "data") — see doc.go's
// "Command-name source" for why this reads the raw files directly rather
// than going through specfmt.Repository/Validate. Non-command specs (none
// exist among the 46 embedded built-ins today, but a future KindPath/
// KindTarget spec sharing the directory would not be one) and
// help-hashes.json itself (which has no "kind":"command" field, so it
// decodes to a zero-value specfmt.Spec and is skipped) are silently
// excluded.
func CommandNames(fsys fs.FS, dataDir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dataDir)
	if err != nil {
		return nil, fmt.Errorf("specdrift: reading %s: %w", dataDir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := dataDir + "/" + e.Name()
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("specdrift: reading %s: %w", path, err)
		}
		var s specfmt.Spec
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("specdrift: decoding %s: %w", path, err)
		}
		if s.Kind != specfmt.KindCommand || s.Command == nil {
			continue
		}
		names = append(names, s.Command.Name)
	}
	sort.Strings(names)
	return names, nil
}

// Record captures a fresh SHA-256 --help hash (via capture) for every non-
// name CommandNames(fsys, dataDir) returns that is not IsExempt on goos, and
// nil for every exempt name — the --record mode's in-memory result, ready for WriteHashes.
func Record(fsys fs.FS, dataDir, goos string, capture HelpCapturer) (map[string]*string, error) {
	names, err := CommandNames(fsys, dataDir)
	if err != nil {
		return nil, err
	}
	hashes := make(map[string]*string, len(names))
	for _, name := range names {
		if IsExempt(goos, name) {
			hashes[name] = nil
			continue
		}
		help, err := capture(name)
		if err != nil {
			return nil, fmt.Errorf("specdrift: recording %q: %w", name, err)
		}
		h := Hash(help)
		hashes[name] = &h
	}
	return hashes, nil
}

// WriteHashes writes hashes as indented JSON to path — the on-disk shape
// this packet's Contract/Produces pins: a flat object mapping command name
// to either a hex SHA-256 string or JSON null. encoding/json sorts map
// keys when marshalling, so the file is deterministic across runs without
// this package doing its own ordering.
func WriteHashes(path string, hashes map[string]*string) error {
	data, err := json.MarshalIndent(hashes, "", "  ")
	if err != nil {
		return fmt.Errorf("specdrift: encoding %s: %w", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("specdrift: writing %s: %w", path, err)
	}
	return nil
}

// LoadPlatformHashes loads the shared baseline (dataDir/HelpHashesFile) and,
// when dataDir/PlatformHashesFile(goos) exists, layers its entries over it.
// A missing overlay is not an error (linux has none).
func LoadPlatformHashes(fsys fs.FS, dataDir, goos string) (map[string]*string, error) {
	base, err := LoadHashes(fsys, dataDir+"/"+HelpHashesFile)
	if err != nil {
		return nil, err
	}
	overlay, err := LoadHashes(fsys, dataDir+"/"+PlatformHashesFile(goos))
	if errors.Is(err, fs.ErrNotExist) {
		return base, nil
	}
	if err != nil {
		return nil, err
	}
	for k, v := range overlay {
		base[k] = v
	}
	return base, nil
}

// Overlay returns the entries of recorded that a per-platform overlay file
// must carry on top of base: every non-nil hash that is absent from or
// differs from base. Entries exempt on goos are never included (their
// baseline is deliberately left to the shared file), and nil entries are
// dropped.
func Overlay(goos string, base, recorded map[string]*string) map[string]*string {
	out := map[string]*string{}
	for name, h := range recorded {
		if h == nil || IsExempt(goos, name) {
			continue
		}
		if b, ok := base[name]; ok && b != nil && *b == *h {
			continue
		}
		out[name] = h
	}
	return out
}

// LoadHashes reads and decodes a help-hashes.json file from fsys at path.
func LoadHashes(fsys fs.FS, path string) (map[string]*string, error) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, fmt.Errorf("specdrift: reading %s: %w", path, err)
	}
	var hashes map[string]*string
	if err := json.Unmarshal(data, &hashes); err != nil {
		return nil, fmt.Errorf("specdrift: decoding %s: %w", path, err)
	}
	return hashes, nil
}

// Drift is one command whose live --help state disagrees with
// help-hashes.json (a missing entry, an exempt/non-exempt mismatch, an
// unreachable binary, or an actual hash mismatch).
type Drift struct {
	Name   string
	Reason string
}

// Check compares CommandNames(fsys, dataDir)'s live --help hash (via
// capture) against recorded (typically LoadHashes' result) for every non-
// exempt name, and returns one Drift per disagreement — empty means clean.
// This is a hash-of-raw-text diff, not a semantic re-derivation of flag
// roles (this packet's own Binding decisions): a mismatch means "the raw
// --help text changed since help-hashes.json was recorded", never an
// automatic re-classification of any flag.
//
// Failure modes, each a distinct Reason:
//   - recorded is missing the name entirely: "missing from help-hashes.json".
//   - recorded[name] is nil for a name NOT in Exempt: the committed file
//     claims an exemption Exempt does not grant.
//   - capture(name) errors (binary not on PATH): the genuine environment-
//     gap failure this packet's Binding decisions calls out — never
//     silently skipped like an Exempt name.
//   - the live hash differs from *recorded[name]: "--help drift".
func Check(fsys fs.FS, dataDir, goos string, recorded map[string]*string, capture HelpCapturer) ([]Drift, error) {
	names, err := CommandNames(fsys, dataDir)
	if err != nil {
		return nil, err
	}
	var drifts []Drift
	for _, name := range names {
		if IsExempt(goos, name) {
			continue
		}
		rec, ok := recorded[name]
		if !ok {
			drifts = append(drifts, Drift{Name: name, Reason: "missing from help-hashes.json"})
			continue
		}
		if rec == nil {
			drifts = append(drifts, Drift{Name: name, Reason: "recorded as exempt (null) in help-hashes.json but is not in the exemption list"})
			continue
		}
		help, err := capture(name)
		if err != nil {
			drifts = append(drifts, Drift{Name: name, Reason: fmt.Sprintf("binary not on PATH: %v", err)})
			continue
		}
		h := Hash(help)
		if h != *rec {
			drifts = append(drifts, Drift{Name: name, Reason: fmt.Sprintf("--help drift: recorded %s, live %s", *rec, h)})
		}
	}
	sort.Slice(drifts, func(i, j int) bool { return drifts[i].Name < drifts[j].Name })
	return drifts, nil
}

package patheval

import (
	"os"
	"path/filepath"
	"strings"
)

// UnattendedExtraReadOnlyRootsEnv names the env var holding the extra read-only
// roots that apply ONLY to an unattended session (permission mode dontAsk, or an
// autonomous ccpool worker: hookio.HookInput.IsUnattended). Same ":"-separated
// absolute-path shape as CETA_EXTRA_READONLY_ROOTS; the home-manager option
// `unattendedExtraReadOnlyRoots` renders it. Bead pg2-b1lir W3, operator ruling
// pg2-r9kv7 (2026-10-27): the pool-only known read roots.
//
// WHY A SEPARATE VARIABLE AND NOT A SESSION-DELIVERED CETA_EXTRA_READONLY_ROOTS.
// The wrapper `--set`s CETA_EXTRA_READONLY_ROOTS, so a role's sessionEnv value
// would be overwritten; and gating on the session's own unattended signal (the
// permission mode in the payload, CCPOOL_AUTONOMOUS in the env) is what makes
// "pool-only" a property of this binary rather than of how a launcher happened to
// set an environment.
const UnattendedExtraReadOnlyRootsEnv = "CETA_UNATTENDED_EXTRA_READONLY_ROOTS"

// unattendedTmpdirPrefixes are the only places a session's $TMPDIR may sit for
// ExtendForUnattended to grant it: the per-user temp tree macOS hands out, /tmp,
// and /private/tmp. A TMPDIR anywhere else (or one that IS a prefix, which would
// grant every user's temp tree) is ignored, never trusted.
var unattendedTmpdirPrefixes = []string{"/private/var/folders/", "/private/tmp/", "/tmp/"}

// ExtendForUnattended widens the evaluator for an unattended session, in two ways:
//
//  1. every root in CETA_UNATTENDED_EXTRA_READONLY_ROOTS becomes a read-only root,
//     except one that is too broad (at or above $HOME) or that touches
//     ~/.claude (the transcripts under ~/.claude/projects can hold secrets, and
//     the ruling excludes them);
//  2. the session's own $TMPDIR (symlink-resolved, and only when it is a
//     directory strictly inside one of unattendedTmpdirPrefixes) becomes a
//     read-write root, so the background-job state under it (bgrun) is readable
//     and a scratch directory made there is writable.
//
// Both land in the same extra-root lists CETA_EXTRA_READONLY_ROOTS and
// CETA_EXTRA_READWRITE_ROOTS fill, so they are checked LAST, after every built-in
// zone and after the secrets and denyRead guards, which run first and cannot be
// widened by a root. Call it once, right after construction and before any
// WithCWD copy; an attended session never calls it.
func (pe *PathEvaluator) ExtendForUnattended() {
	claude := filepath.Join(pe.home, ".claude")
	for _, root := range resolveExtraRoots(UnattendedExtraReadOnlyRootsEnv) {
		if !rootGrantsZone(root, pe.home) {
			continue
		}
		if pe.home != "" && (coversPath(root, claude) || coversPath(claude, root)) {
			continue
		}
		pe.extraReadOnly = append(pe.extraReadOnly, root)
	}
	if dir := unattendedTmpdir(); dir != "" {
		pe.extraReadWrite = append(pe.extraReadWrite, dir)
	}
}

// unattendedTmpdir returns the symlink-resolved $TMPDIR when it is an absolute
// directory strictly inside one of unattendedTmpdirPrefixes, else "".
func unattendedTmpdir() string {
	raw := os.Getenv("TMPDIR")
	if raw == "" || !filepath.IsAbs(raw) {
		return ""
	}
	dir := resolveRefPath(raw)
	if dir == "" {
		return ""
	}
	for _, prefix := range unattendedTmpdirPrefixes {
		if len(dir)+1 > len(prefix) && strings.HasPrefix(dir+"/", prefix) {
			return dir
		}
	}
	return ""
}

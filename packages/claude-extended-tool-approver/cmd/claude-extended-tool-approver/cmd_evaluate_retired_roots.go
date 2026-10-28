package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// Retired roots (pg2-ufkenc, from the pg2-8swr9 findings).
//
// `evaluate` replays every stored row through TODAY's engine and today's
// configured roots (CETA_EXTRA_READWRITE_ROOTS and friends). A row logged
// before a workspace move names a path under a root that no longer exists, so
// its replay abstains ("search path is unknown") while the stored verdict was
// an allow: a false miss that no rule or spec change can fix, because the
// engine is right and the world moved. The 2026-10-15 workspace move produced
// 35 such Grep/Glob rows under /Volumes/ziprecruiter and 6 under
// /Users/phillipg/phillipg_mbp.
//
// A row whose tool input names a path under a retired root, and which would
// otherwise be graded a miss, therefore gets its own terminal category,
// "retired-root": it carries no usable ground truth under today's
// configuration, so it is never counted as a miss and --misses-only drops it.
// Rows under a retired root that replay correct (or needs-review) keep that
// category, so the totals for them do not move. --include-retired switches the
// reclassification off so the rows can still be audited as misses.
//
// This is a reporting filter only: it changes no decision the hook makes and
// adds no rule to internal/rules (frozen, ruling R1 / ADR 0075). A root MUST be
// listed here only when it is gone from the machine for good; a root that is
// merely not configured is a real allowlist gap and MUST stay a miss.

// defaultRetiredRoots are the absolute roots known to be gone for good. Extend
// the list when a later workspace move retires another root, or pass
// --retired-root for a one-off run.
var defaultRetiredRoots = []string{
	// The ZipRecruiter volume, replaced by /Volumes/gitrepos/ziprecruiter in the
	// 2026-10-15 workspace move.
	"/Volumes/ziprecruiter",
	// The pre-move laptop checkout directory of the nix repos.
	"/Users/phillipg/phillipg_mbp",
}

// pathBearingFields are the tool_input string fields that carry a filesystem
// path for every tool that has one (Read/Edit/Write file_path, Grep/Glob path,
// NotebookEdit notebook_path).
var pathBearingFields = []string{"path", "file_path", "notebook_path"}

// retiredRootOf reports the retired root the row's tool input points under, or
// "" when it points under none. Only path-bearing fields are inspected (plus a
// Glob's pattern, which may itself be absolute): free text such as a Bash
// command or an Agent prompt can mention a retired root without acting on it,
// so it MUST NOT classify a row. Unparseable input matches nothing.
func retiredRootOf(toolName, toolInputJSON string, roots []string) string {
	if len(roots) == 0 || toolInputJSON == "" {
		return ""
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(toolInputJSON), &fields); err != nil {
		return ""
	}
	names := pathBearingFields
	if toolName == "Glob" {
		names = append(append([]string{}, names...), "pattern")
	}
	for _, name := range names {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		var p string
		if err := json.Unmarshal(raw, &p); err != nil || !filepath.IsAbs(p) {
			continue
		}
		p = filepath.Clean(p)
		for _, root := range roots {
			root = filepath.Clean(root)
			if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
				return root
			}
		}
	}
	return ""
}

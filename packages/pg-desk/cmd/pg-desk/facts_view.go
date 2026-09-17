package main

import "encoding/json"

// deskPRShow is a minimal decode of an entity row's Facts.PRShow raw JSON
// (gather.Facts, packet 4) — the display fields packet 8's read-side
// commands (open, show) need that packet 5's interpretation row does not
// itself carry (see interpret's own httpapi/server.go doc comment: Phase 9
// has no discrete display columns anywhere in the store; title/url/number/
// author are chosen by gather, not interpret). Mirrors internal/interpret's
// own (unexported) prShow decode struct, kept as an independent, smaller
// copy per this docket's established convention that each package decodes
// only the fields it needs rather than sharing a struct across a package
// boundary (internal/interpret's own package doc comment on gather.go's
// "hand-decode a minimal field subset" precedent).
type deskPRShow struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Author    string `json:"author"`
	Additions int    `json:"additions,omitempty"`
	Deletions int    `json:"deletions,omitempty"`
}

func decodeDeskPRShow(raw json.RawMessage) (deskPRShow, bool) {
	if len(raw) == 0 {
		return deskPRShow{}, false
	}
	var p deskPRShow
	if err := json.Unmarshal(raw, &p); err != nil {
		return deskPRShow{}, false
	}
	return p, true
}

// deskFilesCount decodes an entity row's Facts.PRFiles raw JSON (the `pr
// files` result) and returns how many files changed, or 0 for an empty or
// malformed payload — display-only, never a store or classification value.
func deskFilesCount(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var r struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0
	}
	return len(r.Files)
}

// deskCIStatus computes a coarse CI rollup ("none"|"pending"|"success"|
// "failure") from an entity row's Facts.CI raw JSON (the `ci list` fan-out
// payload: {"runs":[...]}) — DISPLAY only (open's CI column, show's
// rendering), never panel or urgency classification, which stays
// internal/interpret's sole authority (that package's own computeCIRollup
// additionally excludes check_interpreters-matched runs; this display-only
// rollup does not, a deliberate simplification since no acceptance
// criterion pins the rendered CI string, only which panel a row lands in —
// interpret's own Panel field, read from the store as-is).
func deskCIStatus(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "none"
	}
	var fo struct {
		Runs []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(raw, &fo); err != nil {
		return "none"
	}
	var passed, failed, pending int
	for _, r := range fo.Runs {
		if r.Status != "completed" || r.Conclusion == "" || r.Conclusion == "pending" || r.Conclusion == "expected" {
			pending++
			continue
		}
		switch r.Conclusion {
		case "success", "neutral", "skipped":
			passed++
		default:
			failed++
		}
	}
	switch {
	case failed > 0:
		return "failure"
	case pending > 0:
		return "pending"
	case passed > 0:
		return "success"
	default:
		return "none"
	}
}

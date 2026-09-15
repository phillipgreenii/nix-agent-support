// fingerprint.go: the pg-connector-pr-github backend's own version-tagged
// fingerprint CURSOR CODEC (bead pg2-2j5ac.30.3) — a re-port of pg-pr's
// existing PR-change-detector strategy
// (packages/pg-pr/pkg/provider/vcs/github/fingerprint.go's own fingerprint
// composition, and packages/pg-pr/internal/sync/detector.go's
// fingerprintHash) as a set of PURE functions: cursor in, live snapshot
// in, changed-ids + cursor out. No I/O of any kind lives here — this
// backend stays stateless (D3); the removed backend-local stores (temp
// files, an environment-configured state directory, and file locking,
// deleted by phase 7's removals packet) MUST NOT be reintroduced here.
//
// This file does NOT re-derive pg-pr's own "live fingerprint search" GraphQL
// query (the slim per-PR updatedAt/reviews/comments/reviewThreads fetch that
// pg-pr's own fingerprint.go and this package's now-deleted fingerprint.go
// once ran — see internal/vcs/iface.go's own doc comment: "re-derive the
// shape fresh against that verb's actual requirements rather than
// resurrecting this file", bead pg2-lh3c4). Obtaining a live
// []GitHubPRSnapshot to feed RefreshCursor is a caller concern this file
// does not solve.
package github

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// fingerprintCursorVersion is FingerprintCursor's current wire version
// (FingerprintCursor.Version). DecodeCursor treats any other value as
// undecodable (design: section 4.2, "MUST treat an undecodable cursor as
// null").
const fingerprintCursorVersion = 1

// FingerprintCursor is the version-tagged, opaque-to-the-umbrella cursor
// blob this backend returns from list. Only this file encodes/decodes it.
type FingerprintCursor struct {
	Version int               `json:"v"`
	ByPR    map[string]string `json:"fp"` // PR id -> fingerprint hash
}

// GitHubPRSnapshot is this file's own input shape for one live PR, carrying
// exactly the fields pg-pr's own fingerprint composition reads (updated-at,
// head OID, checks rollup, state, draft flag, review and comment counts —
// see packages/pg-pr/internal/sync/detector.go's fingerprintHash and
// packages/pg-pr/pkg/provider/vcs/iface.go's PRFingerprint, which
// packages/pg-pr/pkg/provider/vcs/github/fingerprint.go populates). ID is
// this backend's own entity id (internal/provider.go's formatPRID
// convention, "<owner>/<repo>#<number>"), used as RefreshCursor/
// FingerprintCursor.ByPR's map key — it is NOT part of the hashed content
// (matching PRFingerprint's own Repo/Number exclusion from fingerprintHash).
type GitHubPRSnapshot struct {
	ID                string
	UpdatedAt         string
	HeadOID           string
	StatusRollup      string
	State             string
	IsDraft           bool
	ReviewCount       int
	CommentCount      int
	ReviewThreadCount int
}

// DecodeCursor parses raw (the ledger's stored cursor blob) into a
// FingerprintCursor. An undecodable, wrong-version, or nil raw MUST
// return (nil, nil) — never an error — so the caller treats it as
// "no cursor" (a full fetch), per design: section 4.2, "MUST treat an
// undecodable cursor as null (a full fetch), never as an error."
func DecodeCursor(raw json.RawMessage) (*FingerprintCursor, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var cur FingerprintCursor
	if err := json.Unmarshal(raw, &cur); err != nil {
		return nil, nil
	}
	if cur.Version != fingerprintCursorVersion {
		return nil, nil
	}
	return &cur, nil
}

// EncodeCursor serializes cur back to the opaque blob shape list()
// returns as its response cursor.
func EncodeCursor(cur *FingerprintCursor) (json.RawMessage, error) {
	if cur == nil {
		return nil, nil
	}
	data, err := json.Marshal(cur)
	if err != nil {
		return nil, fmt.Errorf("github: encode fingerprint cursor: %w", err)
	}
	return json.RawMessage(data), nil
}

// ComputeFingerprint returns the same composite hash pg-pr's own
// fingerprint.go computes for one PR (updated-at, head OID, checks
// rollup, state, draft flag, review and comment counts) — re-port the
// same field set and hashing approach; do not invent a different
// composition. Verbatim port of
// packages/pg-pr/internal/sync/detector.go's fingerprintHash: the same
// "%s|%s|%s|%s|%t|%d|%d|%d" composition over
// UpdatedAt/HeadOID/StatusRollup/State/IsDraft/ReviewCount/CommentCount/
// ReviewThreadCount, SHA-256'd and hex-encoded to the first 8 bytes (16
// hex chars).
func ComputeFingerprint(pr GitHubPRSnapshot) string {
	s := fmt.Sprintf("%s|%s|%s|%s|%t|%d|%d|%d",
		pr.UpdatedAt, pr.HeadOID, pr.StatusRollup, pr.State, pr.IsDraft,
		pr.ReviewCount, pr.CommentCount, pr.ReviewThreadCount)
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// RefreshCursor is the pure function this packet's Objective names:
// given the previous cursor (nil means full fetch), the live search
// results, and each result's computed fingerprint, returns which PR ids
// changed since the previous cursor and the new cursor to store. Pure:
// no I/O, no environment-configured-state-directory/temp-file/file-lock
// code — the removed backend-local stores stay removed (phase 7's
// removals packet already deleted that usage from this backend; this
// packet MUST NOT reintroduce any of it).
//
// prev == nil (an undecodable/absent cursor, per DecodeCursor above, or a
// genuinely first call) is treated as a full fetch: every id in live is
// reported changed [design: section 5.2, "a backend that ignores cursors
// and returns everything"]. changedIDs is returned sorted for a
// deterministic, caller-order-independent result.
func RefreshCursor(prev *FingerprintCursor, live []GitHubPRSnapshot) (changedIDs []string, next *FingerprintCursor) {
	byPR := make(map[string]string, len(live))
	for _, pr := range live {
		fp := ComputeFingerprint(pr)
		byPR[pr.ID] = fp
		if prev == nil {
			changedIDs = append(changedIDs, pr.ID)
			continue
		}
		if old, ok := prev.ByPR[pr.ID]; !ok || old != fp {
			changedIDs = append(changedIDs, pr.ID)
		}
	}
	sort.Strings(changedIDs)
	return changedIDs, &FingerprintCursor{Version: fingerprintCursorVersion, ByPR: byPR}
}

package legacyextract

import "strings"

const (
	// preMoveWorkspaceRoot is the real-machine pn workspace root that several
	// legacy test tables quote verbatim. That directory no longer exists, so a
	// row naming it resolved its path against whichever machine replayed the
	// corpus (pg2-ojhog, pg2-j6b0b).
	preMoveWorkspaceRoot = "/Users/phillipg/phillipg_mbp"

	// fakeUnzonedRoot replaces it in every extracted row. The legacy tables
	// quote that root as "a real-machine path that is in NO configured zone"
	// (see the pg2-4k7yd note on TestIntegration_GitDirCensusFalsePositives,
	// whose `find <root> ...` row is pinned NoOpinion for exactly that
	// reason), so the replacement MUST also sit in no zone: /opt is outside
	// the project root, the temp roots and the home tree, and no machine has
	// to provide it. Re-rooting to the temp-root zone (/tmp/ceta-corpus-ws,
	// which corpus.json uses for rows that need an APPROVING zone) would flip
	// that find row to approve and change the row's meaning, not just its
	// spelling.
	fakeUnzonedRoot = "/opt/ceta-corpus-no-zone"
)

// stabilizeWorkspaceRoot rewrites the pre-move workspace root in s to
// fakeUnzonedRoot. It is purely textual and verdict-neutral: the root stays
// an absolute path in no zone, only no longer one that may exist on the
// replaying machine.
func stabilizeWorkspaceRoot(s string) string {
	return strings.ReplaceAll(s, preMoveWorkspaceRoot, fakeUnzonedRoot)
}

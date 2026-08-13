package cmdparse

// SHADOW MODE IS RETIRED (ADR 0039 step 2, pg2-fez3d).
//
// Step 1 (pg2-jxmk9) deliberately ran TWO front ends: the candidate seam alongside
// the authoritative `StripCommentsPreservingHeredocs` + `Parse`, with
// `LogShadowDisagreement` writing diffs to stderr and returning nothing so the
// candidate could not influence a verdict. This step makes the seam authoritative,
// and RETIRING the comparison is how ADR 0039's I8 is discharged: "there MUST NOT
// be a fallback parser", so there must not be a second front end to compare against
// either. Keeping the outgoing one for inputs the new one rejects is precisely the
// two-scanners-that-can-disagree state the decision exists to end.
//
// DELETED with it: `ShadowEnvVar`, `ShadowEnabled`, `ShadowDiff` (and
// `ContentDiffers`/`String`), `OutgoingFrontEnd`, `CompareFrontEnds`,
// `CompareFrontEndsWith`, `pipelineShape`, `shadowLog` and
// `LogShadowDisagreement`. `frontend_ab_test.go` — the A/B latency gate and the
// disagreement census, both of which are comparisons AGAINST the deleted front end
// — is deleted too; the latency gate's result is recorded in LOWERING.md and its
// pass criterion was discharged by step 1.
//
// `LeafKey` SURVIVES, and only it: it is a canonical leaf form with no second front
// end in it, and the I14 coverage check plus the corpus replay harness both key on
// it.

import (
	"fmt"
	"strconv"
	"strings"
)

// LeafKey is a leaf's VERDICT-RELEVANT canonical form: everything a rule or the
// engine can read to reach a decision, and nothing that changes by design in this
// migration.
//
// Deliberately EXCLUDED: Raw (I12 redefines it), Comment (the comment pass is
// retired, and the engine's path leaves it empty on both sides anyway) and the
// pipeline coordinates (a per-call sequence; see ShadowDiff). Each is compared on
// its own so a by-design change cannot hide an accidental one.
func LeafKey(pc ParsedCommand) string {
	var b strings.Builder
	b.WriteString("exec=")
	b.WriteString(strconv.Quote(pc.Executable))
	b.WriteString(" args=[")
	for i, a := range pc.Args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.Quote(a))
	}
	b.WriteString("] env=[")
	for i, e := range pc.EnvVars {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%s/%d", e.Name, strconv.Quote(e.Value), e.Expansion)
	}
	b.WriteString("] redir=[")
	for i, r := range pc.Redirections {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s%s/%d", r.Operator, strconv.Quote(r.Path), r.Kind)
	}
	b.WriteString("] procsub=[")
	for i, p := range pc.ProcessSubstitutions {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.Quote(p))
	}
	fmt.Fprintf(&b, "] heredoc=%v extents=[", pc.HasHeredoc)
	for i, h := range pc.Heredocs {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s/q=%v/t=%v/st=%v/%s",
			h.Delimiter, h.Quoted, h.StripTabs, h.Terminated, strconv.Quote(h.Body))
	}
	b.WriteByte(']')
	// A DATA leaf carries no content at all beyond Raw, so without it every data
	// leaf would key identically and the multiset diff could not tell a `for` word
	// list from a `case` subject. Include Raw for that one shape only.
	if pc.Executable == "" && len(pc.EnvVars) == 0 && len(pc.Redirections) == 0 && !pc.HasHeredoc {
		b.WriteString(" data-raw=")
		b.WriteString(strconv.Quote(pc.Raw))
	}
	return b.String()
}

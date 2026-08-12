package cmdparse

// SHADOW MODE (ADR 0039's Decision; the migration's first step).
//
// Both front ends run. The OUTGOING one — StripCommentsPreservingHeredocs then
// Parse, in that order — stays authoritative for every verdict; the candidate seam
// runs alongside it and disagreements are logged. NO behaviour change ships in this
// step, so nothing here may influence a decision: every function in this file
// either returns a diff or writes to stderr.
//
// This file deliberately does NOT import the parser. Only shellparse.go may (I6).

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ShadowEnvVar names the environment variable that turns the shadow comparison
// off. It is ON by default because the acceptance criterion for this step is that
// both front ends run; the opt-out exists so the hook can be run without the
// second parse when measuring the outgoing front end alone.
const ShadowEnvVar = "CETA_SHADOW_PARSER"

var shadowOnce struct {
	sync.Once
	enabled bool
}

// ShadowEnabled reports whether the shadow comparison runs. The environment is
// read once per process: the hook is short-lived, and re-reading per evaluation
// would put a getenv on the per-leaf path.
func ShadowEnabled() bool {
	shadowOnce.Do(func() {
		shadowOnce.enabled = os.Getenv(ShadowEnvVar) != "0"
	})
	return shadowOnce.enabled
}

// ShadowDiff is one command's disagreement between the two front ends.
//
// Raw, Comment and the pipeline coordinates are counted SEPARATELY from the
// verdict-relevant leaf content, because each of them changes BY DESIGN in this
// migration and folding them into one "differs" bit would bury the changes that
// are not by design:
//
//   - Raw becomes the exact source slice of the owning statement (I12), so every
//     heredoc-bearing leaf's Raw legitimately grows by its body.
//   - Comment stops being derived by a text pass. On the engine's path the
//     outgoing Comment is ALWAYS empty anyway, because the engine pre-strips
//     comments before Parse ever sees them.
//   - PipelineID is a per-call sequence number, so it necessarily renumbers
//     wherever the leaf SET differs; only the induced grouping is comparable.
type ShadowDiff struct {
	// Unparseable reports that the candidate could not parse the command at all.
	// Per I1b this is a floor, and per ADR 0039's Consequences every such row is a
	// forfeiture that MUST be reported in the migration replay.
	Unparseable bool
	Reason      string
	Dialect     string

	OldLeaves int
	NewLeaves int

	// OnlyOld and OnlyNew are the leaf keys present in exactly one front end's
	// output, as a multiset difference. A leaf appearing in both cancels.
	OnlyOld []string
	OnlyNew []string

	// RawDiffers counts leaves matched by content whose Raw text differs.
	RawDiffers int
	// ShapeDiffers reports that the induced pipeline grouping differs.
	ShapeDiffers bool
}

// ContentDiffers reports a disagreement in the verdict-relevant leaf content —
// the only kind that can move a verdict.
func (d ShadowDiff) ContentDiffers() bool {
	return d.Unparseable || len(d.OnlyOld) > 0 || len(d.OnlyNew) > 0
}

func (d ShadowDiff) String() string {
	var b strings.Builder
	if d.Unparseable {
		fmt.Fprintf(&b, "candidate unparseable: %s", d.Reason)
		if d.Dialect != "" {
			fmt.Fprintf(&b, " (supported by: %s)", d.Dialect)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "leaves old=%d new=%d", d.OldLeaves, d.NewLeaves)
	for _, k := range d.OnlyOld {
		fmt.Fprintf(&b, "\n  -old %s", k)
	}
	for _, k := range d.OnlyNew {
		fmt.Fprintf(&b, "\n  +new %s", k)
	}
	if d.RawDiffers > 0 {
		fmt.Fprintf(&b, "\n  raw-differs=%d", d.RawDiffers)
	}
	if d.ShapeDiffers {
		b.WriteString("\n  pipeline-shape-differs")
	}
	return b.String()
}

// OutgoingFrontEnd is the outgoing front end as ONE named function, so the shadow
// comparison, the A/B benchmark and the engine cannot drift about what "outgoing"
// means. It is exactly StripCommentsPreservingHeredocs then Parse, in that order.
func OutgoingFrontEnd(expr string) []ParsedCommand {
	return Parse(StripCommentsPreservingHeredocs(expr))
}

// CompareFrontEnds runs BOTH front ends over expr and returns their disagreement.
func CompareFrontEnds(expr string) ShadowDiff {
	return CompareFrontEndsWith(expr, OutgoingFrontEnd(expr))
}

// CompareFrontEndsWith is CompareFrontEnds for a caller that has already run the
// outgoing front end and must not pay for it twice (the engine).
func CompareFrontEndsWith(expr string, old []ParsedCommand) ShadowDiff {
	sp := ParseShell(expr)
	if sp.Unparseable {
		return ShadowDiff{
			Unparseable: true, Reason: sp.Reason, Dialect: sp.Dialect,
			OldLeaves: len(old),
		}
	}
	d := ShadowDiff{OldLeaves: len(old), NewLeaves: len(sp.Leaves)}

	oldKeys := make(map[string][]int, len(old))
	for i, pc := range old {
		k := LeafKey(pc)
		oldKeys[k] = append(oldKeys[k], i)
	}
	matchedOld := make([]bool, len(old))
	for _, pc := range sp.Leaves {
		k := LeafKey(pc)
		idxs := oldKeys[k]
		if len(idxs) == 0 {
			d.OnlyNew = append(d.OnlyNew, k)
			continue
		}
		i := idxs[0]
		oldKeys[k] = idxs[1:]
		matchedOld[i] = true
		if old[i].Raw != pc.Raw {
			d.RawDiffers++
		}
	}
	for i, pc := range old {
		if !matchedOld[i] {
			d.OnlyOld = append(d.OnlyOld, LeafKey(pc))
		}
	}
	sort.Strings(d.OnlyOld)
	sort.Strings(d.OnlyNew)
	d.ShapeDiffers = pipelineShape(old) != pipelineShape(sp.Leaves)
	return d
}

// pipelineShape reduces a leaf set to its pipeline GROUPING, which is comparable
// across front ends where the raw per-call ID numbers are not: for each pipeline,
// the number of stages, sorted.
func pipelineShape(leaves []ParsedCommand) string {
	sizes := map[int]int{}
	for _, pc := range leaves {
		if pc.PipelineID < 0 {
			continue
		}
		sizes[pc.PipelineID]++
	}
	out := make([]int, 0, len(sizes))
	for _, n := range sizes {
		out = append(out, n)
	}
	sort.Ints(out)
	var b strings.Builder
	for _, n := range out {
		b.WriteString(strconv.Itoa(n))
		b.WriteByte(',')
	}
	return b.String()
}

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

// shadowLog is where LogShadowDisagreement writes. It is a variable so a test can
// capture the output without touching the process's stderr.
var shadowLog io.Writer = os.Stderr

// LogShadowDisagreement runs the candidate front end against an outgoing result
// the caller already has and logs any VERDICT-RELEVANT disagreement to stderr.
//
// It returns nothing on purpose: in this step the candidate MUST NOT be able to
// influence a decision. The old verdict is authoritative.
func LogShadowDisagreement(expr string, old []ParsedCommand) {
	if !ShadowEnabled() {
		return
	}
	d := CompareFrontEndsWith(expr, old)
	if !d.ContentDiffers() {
		return
	}
	fmt.Fprintf(shadowLog, "claude-extended-tool-approver: shadow-parse disagreement: %s\n", d)
}

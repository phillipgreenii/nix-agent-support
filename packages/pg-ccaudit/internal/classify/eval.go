package classify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-ccaudit/internal/candidate"
	"github.com/phillipgreenii/pg-ccaudit/internal/gold"
)

// MinGoldEntries is the floor criterion 4 sets on the evaluation set.
//
// Below it the per-class figures are arithmetic rather than evidence: a class with
// two examples reports precision 0.5 or 1.0 and nothing in between, which reads
// like a measurement and is not one. Evaluate reports UnderSized rather than
// refusing, because a smaller run is still useful while labelling is in progress —
// but the flag MUST be carried into any report that quotes the numbers.
const MinGoldEntries = 50

// Metrics is one class's confusion-derived scores.
type Metrics struct {
	Class     Class   `json:"class"`
	Support   int     `json:"support"` // gold entries of this class
	Predicted int     `json:"predicted"`
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	FN        int     `json:"fn"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
}

// Binary is the correction/not-a-correction collapse — the axis the Baseline
// classifier competes on, because "every typed turn following a tool call is a
// correction" makes no finer distinction. Comparing a multi-class classifier
// against it on multi-class accuracy would be comparing it against a rule that
// cannot play, so the comparison is made where both can.
type Binary struct {
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	FN        int     `json:"fn"`
	TN        int     `json:"tn"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
}

// Evaluation is one classifier's score against the gold set.
type Evaluation struct {
	Classifier    string     `json:"classifier"`
	PromptVersion int        `json:"prompt_version"`
	Scored        int        `json:"scored"`
	Unscored      int        `json:"unscored"`
	UnderSized    bool       `json:"under_sized"`
	PerClass      []Metrics  `json:"per_class"`
	Accuracy      float64    `json:"accuracy"`
	Correction    Binary     `json:"correction"`
	Labellers     []string   `json:"labellers"`
	FileChannel   int        `json:"file_channel"`
	Marker        MarkerStat `json:"marker"`
}

// MarkerStat is the `Correction:` stem's measured RECALL, which the bead makes
// mandatory rather than optional.
//
// The marker rule (M-1..M-3) makes Tier 1 cheap and precise, but its COMPLIANCE is
// unmeasured by construction: if agents comply 60% of the time the count is 60% of
// acknowledgments, and that fraction drifts with every model and prompt change —
// reintroducing exactly the drift the marker was meant to remove. So the marker
// never replaces the semantic pass; the semantic pass is what keeps the marker
// calibrated. Two numbers, and the DENOMINATOR is what makes them honest:
//
//	Mistakes         classifications the semantic pass called a mistake
//	MistakesWithAck  those with an acknowledgment candidate in the same session
//	                 within AckWindow lines AFTER the evidence
//
// A LOW recall means agents are not marking, NOT that mistakes are rare. A rise
// after the rule's adoption date is a MARKING artifact and MUST NOT be read as a
// rise in mistakes (M-2 forbids the rule from changing acknowledgment frequency).
type MarkerStat struct {
	Mistakes        int     `json:"mistakes"`
	MistakesWithAck int     `json:"mistakes_with_ack"`
	Recall          float64 `json:"recall"`
	AckWindow       int64   `json:"ack_window"`
	AckCandidates   int     `json:"ack_candidates"`
	CorrectionStems int     `json:"correction_stems"`
}

// AckWindow is how many line ordinals after a candidate an acknowledgment still
// counts as being about it. 40 lines is roughly the same span escaping-retries
// uses, and comfortably wider than the measured 15-line median distance between a
// human turn and the tool call it answers.
const AckWindow = int64(40)

// Evaluate scores classifications against the gold set.
//
// Only gold entries that MATCH a classification are scored; the rest are counted as
// Unscored. That asymmetry is deliberate — a gold entry with no classification
// usually means the candidate fell outside the run's window or bound, which is a
// coverage fact, not a classifier error, and folding it in as a miss would blame
// the classifier for the operator's --max flag.
func Evaluate(name string, g gold.Set, got []Classification, all []candidate.Candidate) Evaluation {
	byID := make(map[string]Classification, len(got))
	for _, c := range got {
		byID[CandidateID(c.Candidate)] = c
	}

	ev := Evaluation{
		Classifier:    name,
		PromptVersion: PromptVersion,
		FileChannel:   len(g.FileChannel()),
	}

	type pair struct {
		want Class
		have Class
	}
	var pairs []pair
	labellers := map[string]bool{}
	for _, e := range g.Labelled() {
		labellers[e.Labeller] = true
		c, ok := byID[e.ID]
		if !ok {
			ev.Unscored++
			continue
		}
		want, err := ParseClass(e.Class)
		if err != nil {
			// An unparseable gold label is counted as unscored rather than silently
			// mapped to a class; scoring against a label nobody can interpret would
			// manufacture a number.
			ev.Unscored++
			continue
		}
		pairs = append(pairs, pair{want: want, have: c.Class})
	}
	ev.Scored = len(pairs)
	ev.UnderSized = ev.Scored < MinGoldEntries
	for l := range labellers {
		if l != "" {
			ev.Labellers = append(ev.Labellers, l)
		}
	}
	sort.Strings(ev.Labellers)

	correct := 0
	for _, cl := range Classes {
		m := Metrics{Class: cl}
		for _, p := range pairs {
			switch {
			case p.want == cl && p.have == cl:
				m.TP++
			case p.want != cl && p.have == cl:
				m.FP++
			case p.want == cl && p.have != cl:
				m.FN++
			}
		}
		m.Support = m.TP + m.FN
		m.Predicted = m.TP + m.FP
		m.Precision = ratio(m.TP, m.TP+m.FP)
		m.Recall = ratio(m.TP, m.TP+m.FN)
		m.F1 = f1(m.Precision, m.Recall)
		ev.PerClass = append(ev.PerClass, m)
		correct += m.TP
	}
	ev.Accuracy = ratio(correct, ev.Scored)

	for _, p := range pairs {
		switch {
		case p.want.IsCorrection() && p.have.IsCorrection():
			ev.Correction.TP++
		case !p.want.IsCorrection() && p.have.IsCorrection():
			ev.Correction.FP++
		case p.want.IsCorrection() && !p.have.IsCorrection():
			ev.Correction.FN++
		default:
			ev.Correction.TN++
		}
	}
	ev.Correction.Precision = ratio(ev.Correction.TP, ev.Correction.TP+ev.Correction.FP)
	ev.Correction.Recall = ratio(ev.Correction.TP, ev.Correction.TP+ev.Correction.FN)
	ev.Correction.F1 = f1(ev.Correction.Precision, ev.Correction.Recall)

	ev.Marker = markerRecall(got, all)
	return ev
}

func markerRecall(got []Classification, all []candidate.Candidate) MarkerStat {
	st := MarkerStat{AckWindow: AckWindow}
	type ackRef struct {
		seq  int64
		stem bool
	}
	acks := map[string][]ackRef{}
	for _, c := range all {
		if c.Signal != candidate.Ack {
			continue
		}
		st.AckCandidates++
		stem := c.Kind == "correction-marker"
		if stem {
			st.CorrectionStems++
		}
		acks[c.SessionID] = append(acks[c.SessionID], ackRef{seq: c.Seq, stem: stem})
	}
	for _, cl := range got {
		if !cl.Class.IsMistake() || cl.Candidate.Supplementary {
			continue
		}
		st.Mistakes++
		for _, a := range acks[cl.Candidate.SessionID] {
			if a.seq >= cl.Candidate.Seq && a.seq <= cl.Candidate.Seq+AckWindow {
				st.MistakesWithAck++
				break
			}
		}
	}
	st.Recall = ratio(st.MistakesWithAck, st.Mistakes)
	return st
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func f1(p, r float64) float64 {
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// Comparison is the criterion-4 verdict: did the semantic classifier beat the
// naive rule on the same gold set?
type Comparison struct {
	Candidate Evaluation `json:"candidate"`
	Baseline  Evaluation `json:"baseline"`
	// Beats is decided on the CORRECTION F1, not accuracy. Accuracy is dominated by
	// the not-a-mistake majority — a classifier answering "not-a-mistake" to
	// everything scores well on it — so accuracy cannot express "better at finding
	// corrections", which is the only thing this census wants.
	Beats  bool   `json:"beats_baseline"`
	Reason string `json:"reason"`
}

// Compare produces the criterion-4 verdict. A classifier that does not beat the
// baseline MUST be reworked rather than shipped, so the verdict is returned as data
// and rendered, not buried in a log line.
func Compare(candidateEval, baselineEval Evaluation) Comparison {
	c := Comparison{Candidate: candidateEval, Baseline: baselineEval}
	switch {
	case candidateEval.Scored == 0:
		c.Beats = false
		c.Reason = "no gold entry matched a classification — nothing was measured"
	case candidateEval.Correction.F1 > baselineEval.Correction.F1:
		c.Beats = true
		c.Reason = fmt.Sprintf("correction F1 %.3f > baseline %.3f (precision %.3f vs %.3f, recall %.3f vs %.3f)",
			candidateEval.Correction.F1, baselineEval.Correction.F1,
			candidateEval.Correction.Precision, baselineEval.Correction.Precision,
			candidateEval.Correction.Recall, baselineEval.Correction.Recall)
	default:
		c.Beats = false
		c.Reason = fmt.Sprintf("correction F1 %.3f does NOT beat baseline %.3f — rework, do not ship",
			candidateEval.Correction.F1, baselineEval.Correction.F1)
	}
	if candidateEval.UnderSized {
		c.Reason += fmt.Sprintf("; gold set UNDER-SIZED (%d scored, criterion 4 requires %d)",
			candidateEval.Scored, MinGoldEntries)
	}
	return c
}

// Render writes a comparison as a report section.
func Render(sb *strings.Builder, c Comparison) {
	fmt.Fprintf(sb, "Tier 2 evaluation — gold set: %d scored, %d unscored, %d file-channel corrections\n",
		c.Candidate.Scored, c.Candidate.Unscored, c.Candidate.FileChannel)
	if len(c.Candidate.Labellers) > 0 {
		fmt.Fprintf(sb, "  labelled by: %s\n", strings.Join(c.Candidate.Labellers, "; "))
	}
	if c.Candidate.UnderSized {
		fmt.Fprintf(sb, "  WARNING: fewer than %d scored entries — per-class figures are arithmetic, not evidence\n",
			MinGoldEntries)
	}
	fmt.Fprintf(sb, "\n  %-24s %7s %9s %7s %7s %7s %7s %7s\n",
		"class", "support", "predicted", "tp", "fp", "fn", "prec", "recall")
	for _, m := range c.Candidate.PerClass {
		if m.Support == 0 && m.Predicted == 0 {
			continue
		}
		fmt.Fprintf(sb, "  %-24s %7d %9d %7d %7d %7d %7.3f %7.3f\n",
			m.Class, m.Support, m.Predicted, m.TP, m.FP, m.FN, m.Precision, m.Recall)
	}
	fmt.Fprintf(sb, "\n  accuracy %.3f (%s)\n", c.Candidate.Accuracy, c.Candidate.Classifier)
	fmt.Fprintf(sb, "  correction  F1 %.3f  precision %.3f  recall %.3f\n",
		c.Candidate.Correction.F1, c.Candidate.Correction.Precision, c.Candidate.Correction.Recall)
	fmt.Fprintf(sb, "  baseline    F1 %.3f  precision %.3f  recall %.3f   (rule: every typed turn following a tool call is a correction)\n",
		c.Baseline.Correction.F1, c.Baseline.Correction.Precision, c.Baseline.Correction.Recall)
	verdict := "FAIL"
	if c.Beats {
		verdict = "PASS"
	}
	fmt.Fprintf(sb, "  criterion 4: %s — %s\n", verdict, c.Reason)

	m := c.Candidate.Marker
	fmt.Fprintf(sb, "\n  marker recall (M-1 `Correction:` stem): %d/%d = %.3f within %d lines\n",
		m.MistakesWithAck, m.Mistakes, m.Recall, m.AckWindow)
	fmt.Fprintf(sb, "    %d acknowledgment candidates, %d of them the `Correction:` stem\n",
		m.AckCandidates, m.CorrectionStems)
	fmt.Fprintln(sb, "    This is MARKER COMPLIANCE, not a mistake rate. A low value means agents are not")
	fmt.Fprintln(sb, "    marking; it does NOT mean mistakes are rare. The stem is forward-only from")
	fmt.Fprintln(sb, "    2026-07-30 and M-2 forbids it changing acknowledgment FREQUENCY, so a rise across")
	fmt.Fprintln(sb, "    that boundary is a marking artifact and MUST NOT be read as a rise in mistakes.")
}

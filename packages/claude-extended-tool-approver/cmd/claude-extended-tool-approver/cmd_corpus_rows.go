package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
	"github.com/spf13/cobra"
)

// corpusRowsOpts holds the flags of the corpus-rows subcommand.
type corpusRowsOpts struct {
	packet        int
	families      []string
	shapesFrom    string
	replay        string
	db            string
	since         string
	corpus        string
	out           string
	appendRows    bool
	needsJudgment string
	labels        string
	src           sourceFlags
}

func newCorpusRowsCmd() *cobra.Command {
	var o corpusRowsOpts
	cmd := &cobra.Command{
		Use:   "corpus-rows",
		Short: "Propose golden-corpus rows for the in-scope shapes of a replay (read-only unless --append)",
		Long: `corpus-rows reads ART/replay.jsonl and proposes one goldencorpus.Row per selected
shape, built from the shape's representative (lowest-id) row in the ask log.
ART is ${XDG_STATE_HOME:-$HOME/.local/state}/claude-extended-tool-approver/phase4/.

A shape is selected when it is named by --family (the replay family value;
repeatable) or by --shapes-from (a file with one shape_key per line), is not
state_dependent, its new_decision is abstain or differs from its old_decision
(old ask counts as abstain; an unrecorded old decision never disagrees), and
the --corpus file has no row tagged shape:<shape_key>.

expected_verdict is the recorded old decision: approve is approve, reject is
reject, and abstain, ask and unrecorded are not-approve. The generator never
approves on its own judgment: a selected shape whose new_decision is approve
while its old_decision is not approve is not emitted. It is written to
--needs-judgment (JSON Lines; stderr when omitted) for the caller to label.
--labels (JSON Lines: shape_key, expected_verdict, explanation beginning
LLM-proposed: or operator-reviewed:) supplies those labels; a labeled shape is
emitted as a row on the next run.

Rows are written as a JSON array (--out, default stdout), ordered by rows
descending then shape_key ascending; two runs over the same inputs are
byte-identical. --append instead splices the rows after the last row of
--corpus, leaving every existing byte unchanged, and writes nothing else.
String values of tool_input that hold an obvious credential are redacted.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCorpusRows(cmd.OutOrStdout(), cmd.ErrOrStderr(), o)
		},
	}
	f := cmd.Flags()
	f.IntVar(&o.packet, "packet", 0, "The calling packet's number (final integer of its bead id); required")
	f.StringArrayVar(&o.families, "family", nil, "Select shapes whose replay family equals NAME (repeatable)")
	f.StringVar(&o.shapesFrom, "shapes-from", "", "File with one shape_key per line; selects those shapes")
	f.StringVar(&o.replay, "replay", "", "Replay file (default: ART/replay.jsonl)")
	f.StringVar(&o.db, "db", "", "Ask-log database path for --source ceta (default: the ask log's own default path)")
	f.StringVar(&o.since, "since", "", "Common flag; accepted and does not change selection")
	f.StringVar(&o.corpus, "corpus", "", "Golden corpus read for the already-claimed check and the target of --append (default: "+defaultReplayCorpus+")")
	f.StringVar(&o.out, "out", "", "Write the proposed rows as a JSON array to FILE (default: stdout)")
	f.BoolVar(&o.appendRows, "append", false, "Splice the rows after the last row of --corpus; write nothing else")
	f.StringVar(&o.needsJudgment, "needs-judgment", "", "Write needs-judgment shapes as JSON Lines to FILE (default: stderr)")
	f.StringVar(&o.labels, "labels", "", "JSON Lines labels (shape_key, expected_verdict, explanation) for needs-judgment shapes")
	addSourceFlags(cmd, &o.src)
	return cmd
}

// needsJudgmentEntry is one line of the --needs-judgment output.
type needsJudgmentEntry struct {
	ShapeKey     string `json:"shape_key"`
	OldDecision  string `json:"old_decision"`
	NewDecision  string `json:"new_decision"`
	ExampleRowID int    `json:"example_row_id"`
	Reason       string `json:"reason"`
}

const needsJudgmentReason = "new engine approves without recorded approve"

// corpusLabel is one line of the --labels file.
type corpusLabel struct {
	ShapeKey        string `json:"shape_key"`
	ExpectedVerdict string `json:"expected_verdict"`
	Explanation     string `json:"explanation"`
}

// readJSONLines decodes every non-blank line of path into a new T.
func readJSONLines[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []T
	br := bufio.NewReaderSize(f, 1<<20)
	for n := 1; ; n++ {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e T
			if jerr := json.Unmarshal(line, &e); jerr != nil {
				return nil, fmt.Errorf("parse %s line %d: %w", path, n, jerr)
			}
			out = append(out, e)
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// readShapeKeys reads a --shapes-from file: one shape_key per line, blank
// lines ignored.
func readShapeKeys(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimRight(l, "\r"); strings.TrimSpace(l) != "" {
			keys = append(keys, l)
		}
	}
	return keys, nil
}

// readCorpusRows parses the corpus file and returns the set of shape keys
// already claimed by a shape:<shape_key> tag.
func readCorpusClaims(data []byte) (map[string]bool, error) {
	var rows []goldencorpus.Row
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	claimed := map[string]bool{}
	for _, r := range rows {
		for _, t := range r.Tags {
			if k, ok := strings.CutPrefix(t, "shape:"); ok {
				claimed[k] = true
			}
		}
	}
	return claimed, nil
}

// Credential redaction (docket "Corpus row construction"): only the secret
// value is replaced, keeping the key name, scheme word or flag.
var (
	credKeyRE  = regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password|passwd|bearer)[=: ]+["']?)([^\s"']+)`)
	credAuthRE = regexp.MustCompile(`(?i)(Authorization:\s*(?:[A-Za-z]+\s+)?)([A-Za-z0-9+/_=-]{20,})`)
	credUserRE = regexp.MustCompile(`(^|\s)(-u\s+)(\S+)`)
	credRunRE  = regexp.MustCompile(`^[A-Za-z0-9+/_=-]{20,}$`)
)

const redacted = "REDACTED"

func redactString(s string) string {
	s = credKeyRE.ReplaceAllString(s, "${1}"+redacted)
	s = credAuthRE.ReplaceAllString(s, "${1}"+redacted)
	return credUserRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := credUserRE.FindStringSubmatch(m)
		// -u user:pass, or a 20+ character token; any other -u argument
		// (sort -u, git push -u origin) is not a credential.
		if strings.Contains(sub[3], ":") || credRunRE.MatchString(sub[3]) {
			return sub[1] + sub[2] + redacted
		}
		return m
	})
}

// redactValue applies redactString to every string value of a decoded JSON
// value (map keys are left alone).
func redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return redactString(t)
	case []any:
		for i := range t {
			t[i] = redactValue(t[i])
		}
		return t
	case map[string]any:
		for k := range t {
			t[k] = redactValue(t[k])
		}
		return t
	}
	return v
}

// corpusMode maps a recorded permission mode to a corpus mode.
func corpusMode(m string) goldencorpus.Mode {
	switch mode := goldencorpus.Mode(m); mode {
	case goldencorpus.ModeAcceptEdits, goldencorpus.ModePlan, goldencorpus.ModeBypassPermissions:
		return mode
	}
	return goldencorpus.ModeDefault
}

// verdictForOld is the pinned mapping of a recorded decision.
func verdictForOld(old string) goldencorpus.Verdict {
	switch old {
	case "approve":
		return goldencorpus.Approve
	case "reject":
		return goldencorpus.Reject
	}
	return goldencorpus.NotApprove
}

func shapeHash12(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

type proposedRow struct {
	rows int
	key  string
	row  goldencorpus.Row
}

// buildCorpusRow applies the pinned construction rule to a representative row.
func buildCorpusRow(packet int, e replayEntry, rep repRow, verdict goldencorpus.Verdict, notes string) (goldencorpus.Row, error) {
	var input map[string]any
	if err := json.Unmarshal([]byte(rep.input.ToolInputJSON), &input); err != nil {
		return goldencorpus.Row{}, fmt.Errorf("shape %s: representative row tool input is not a JSON object: %w", e.ShapeKey, err)
	}
	if input == nil {
		input = map[string]any{}
	}
	redactValue(input)
	return goldencorpus.Row{
		Case:            fmt.Sprintf("spec%d-%s", packet, shapeHash12(e.ShapeKey)),
		Tags:            []string{"shape:" + e.ShapeKey, "family:" + e.Family, "spec"},
		ToolInput:       goldencorpus.ToolInputFixture{ToolName: rep.input.ToolName, ToolInput: input},
		CWDPathState:    goldencorpus.PathStateFixture{CWD: rep.input.CWD},
		Mode:            corpusMode(rep.input.PermissionMode),
		ExpectedVerdict: verdict,
		Notes:           notes,
	}, nil
}

// marshalRows renders rows as an indented JSON array (no HTML escaping).
func marshalRows(rows []goldencorpus.Row, prefix string) ([]byte, error) {
	var buf bytes.Buffer
	for i, r := range rows {
		var one bytes.Buffer
		enc := json.NewEncoder(&one)
		enc.SetEscapeHTML(false)
		enc.SetIndent(prefix+"  ", "  ")
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
		if i > 0 {
			buf.WriteString(",\n")
		}
		buf.WriteString(prefix + "  ")
		buf.Write(bytes.TrimRight(one.Bytes(), "\n"))
	}
	return buf.Bytes(), nil
}

// spliceRows inserts rows after the last row object of a corpus array,
// leaving every existing byte before the closing bracket unchanged.
func spliceRows(corpus []byte, rows []goldencorpus.Row) ([]byte, error) {
	if len(rows) == 0 {
		return corpus, nil
	}
	end := bytes.LastIndexByte(corpus, ']')
	if end < 0 {
		return nil, errors.New("corpus file has no closing bracket")
	}
	body := bytes.TrimRight(corpus[:end], " \t\r\n")
	if len(body) == 0 || body[len(body)-1] == '[' {
		return nil, errors.New("corpus file has no existing row to append after")
	}
	enc, err := marshalRows(rows, "")
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(body)
	out.WriteString(",\n")
	out.Write(enc)
	out.Write(corpus[len(body):end:end])
	out.Write(corpus[end:])
	return out.Bytes(), nil
}

func runCorpusRows(stdout, stderr io.Writer, o corpusRowsOpts) error {
	if err := o.src.validate(); err != nil {
		return usageErrorf("%v", err)
	}
	if o.packet <= 0 {
		return usageErrorf("--packet N is required (the final integer of the packet's bead id)")
	}
	if len(o.families) == 0 && o.shapesFrom == "" {
		return usageErrorf("select shapes with --family and/or --shapes-from")
	}

	dir, err := artDir()
	if err != nil {
		return err
	}
	replayPath := o.replay
	if replayPath == "" {
		replayPath = filepath.Join(dir, "replay.jsonl")
	}
	corpusPath := o.corpus
	if corpusPath == "" {
		corpusPath = defaultReplayCorpus
	}

	entries, err := readJSONLines[replayEntry](replayPath)
	if err != nil {
		return fmt.Errorf("read replay file: %w", err)
	}
	corpusBytes, err := os.ReadFile(corpusPath)
	if err != nil {
		return fmt.Errorf("read corpus file: %w", err)
	}
	claimed, err := readCorpusClaims(corpusBytes)
	if err != nil {
		return fmt.Errorf("parse corpus file %s: %w", corpusPath, err)
	}
	var wantKeys []string
	if o.shapesFrom != "" {
		if wantKeys, err = readShapeKeys(o.shapesFrom); err != nil {
			return fmt.Errorf("read --shapes-from file: %w", err)
		}
	}
	var labels []corpusLabel
	if o.labels != "" {
		if labels, err = readJSONLines[corpusLabel](o.labels); err != nil {
			return fmt.Errorf("read --labels file: %w", err)
		}
	}

	byKey := map[string]replayEntry{}
	for _, e := range entries {
		byKey[e.ShapeKey] = e
	}
	inFamily := map[string]bool{}
	for _, f := range o.families {
		hit := false
		for _, e := range entries {
			if e.Family == f {
				hit = true
				break
			}
		}
		if !hit {
			return exitCodeError{code: 2}.withMsg(stderr, "--family %q matches no shape in %s", f, replayPath)
		}
		inFamily[f] = true
	}
	selectedKey := map[string]bool{}
	for _, k := range wantKeys {
		if _, ok := byKey[k]; !ok {
			return exitCodeError{code: 2}.withMsg(stderr, "--shapes-from key %q is not in %s", k, replayPath)
		}
		selectedKey[k] = true
	}

	// Select and classify.
	var direct, judgment []replayEntry
	for _, e := range entries {
		if (!inFamily[e.Family] && !selectedKey[e.ShapeKey]) || e.StateDependent || claimed[e.ShapeKey] {
			continue
		}
		old := e.OldDecision
		if old == "ask" {
			old = "abstain"
		}
		disagrees := old != decisionUnrecorded && e.NewDecision != old
		if e.NewDecision != "abstain" && !disagrees {
			continue
		}
		if e.NewDecision == "approve" && e.OldDecision != "approve" {
			judgment = append(judgment, e)
		} else {
			direct = append(direct, e)
		}
	}

	// Labels apply only to shapes in the needs-judgment output.
	labelFor := map[string]corpusLabel{}
	inJudgment := map[string]bool{}
	for _, e := range judgment {
		inJudgment[e.ShapeKey] = true
	}
	for _, l := range labels {
		switch {
		case !inJudgment[l.ShapeKey]:
			return exitCodeError{code: 2}.withMsg(stderr, "--labels key %q is not a needs-judgment shape of this selection", l.ShapeKey)
		case !goldencorpus.Verdict(l.ExpectedVerdict).Valid():
			return exitCodeError{code: 2}.withMsg(stderr, "--labels key %q has expected_verdict %q, want approve, reject or not-approve", l.ShapeKey, l.ExpectedVerdict)
		case !strings.HasPrefix(l.Explanation, llmProposedPrefix) && !strings.HasPrefix(l.Explanation, operatorReviewedPrefix):
			return exitCodeError{code: 2}.withMsg(stderr, "--labels key %q: explanation must begin %q or %q", l.ShapeKey, llmProposedPrefix, operatorReviewedPrefix)
		}
		labelFor[l.ShapeKey] = l
	}

	// Representative rows.
	toEmit := append([]replayEntry{}, direct...)
	var pending []needsJudgmentEntry
	for _, e := range judgment {
		if _, ok := labelFor[e.ShapeKey]; ok {
			toEmit = append(toEmit, e)
			continue
		}
		id, _ := minID(e.ExampleRowIDs)
		pending = append(pending, needsJudgmentEntry{
			ShapeKey: e.ShapeKey, OldDecision: e.OldDecision, NewDecision: e.NewDecision,
			ExampleRowID: id, Reason: needsJudgmentReason,
		})
	}
	var ids []int
	for _, e := range toEmit {
		id, ok := minID(e.ExampleRowIDs)
		if !ok {
			return fmt.Errorf("shape %s has no example row ids", e.ShapeKey)
		}
		ids = append(ids, id)
	}
	rep, err := lookupRepRows(replayOpts{since: o.since, db: o.db, src: o.src}, ids)
	if err != nil {
		return err
	}

	proposed := make([]proposedRow, 0, len(toEmit))
	for _, e := range toEmit {
		id, _ := minID(e.ExampleRowIDs)
		r, ok := rep[id]
		if !ok {
			return fmt.Errorf("shape %s: representative row %d not found in the ask log", e.ShapeKey, id)
		}
		verdict, notes := verdictForOld(e.OldDecision), "shape "+e.ShapeKey
		if l, ok := labelFor[e.ShapeKey]; ok {
			verdict, notes = goldencorpus.Verdict(l.ExpectedVerdict), "shape "+e.ShapeKey+"; "+l.Explanation
		}
		row, err := buildCorpusRow(o.packet, e, r, verdict, notes)
		if err != nil {
			return err
		}
		proposed = append(proposed, proposedRow{rows: e.Rows, key: e.ShapeKey, row: row})
	}
	sort.Slice(proposed, func(i, j int) bool {
		if proposed[i].rows != proposed[j].rows {
			return proposed[i].rows > proposed[j].rows
		}
		return proposed[i].key < proposed[j].key
	})
	sort.Slice(pending, func(i, j int) bool { return needsLess(byKey, pending[i], pending[j]) })
	rows := make([]goldencorpus.Row, len(proposed))
	for i, p := range proposed {
		rows[i] = p.row
	}

	// Needs-judgment output.
	nj := stderr
	if o.needsJudgment != "" {
		f, err := os.Create(o.needsJudgment)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		nj = f
	}
	enc := json.NewEncoder(nj)
	enc.SetEscapeHTML(false)
	for _, p := range pending {
		if err := enc.Encode(p); err != nil {
			return err
		}
	}

	if o.appendRows {
		out, err := spliceRows(corpusBytes, rows)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := os.WriteFile(corpusPath, out, 0o644); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintf(stderr, "appended rows=%d needs_judgment=%d corpus=%s\n", len(rows), len(pending), corpusPath)
		return nil
	}

	body := []byte("[]\n")
	if len(rows) > 0 {
		enc, err := marshalRows(rows, "")
		if err != nil {
			return err
		}
		body = append(append([]byte("[\n"), enc...), []byte("\n]\n")...)
	}
	if o.out != "" {
		return os.WriteFile(o.out, body, 0o644)
	}
	_, err = stdout.Write(body)
	return err
}

// needsLess orders needs-judgment entries by rows descending then shape_key.
func needsLess(byKey map[string]replayEntry, a, b needsJudgmentEntry) bool {
	ra, rb := byKey[a.ShapeKey].Rows, byKey[b.ShapeKey].Rows
	if ra != rb {
		return ra > rb
	}
	return a.ShapeKey < b.ShapeKey
}

// withMsg prints a message naming the offending input on stderr and returns
// the receiver, which main maps to its exit code.
func (e exitCodeError) withMsg(stderr io.Writer, format string, args ...any) error {
	_, _ = fmt.Fprintf(stderr, "error: "+format+"\n", args...)
	return e
}

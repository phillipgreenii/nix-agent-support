package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/links"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// attentionNow is the clock seam of the attention verbs; tests pin it.
var attentionNow interpret.Clock = interpret.SystemClock{}

// attentionCmdFlags is a named type so tests can reset it between runs.
type attentionCmdFlags struct {
	listJSON    bool
	explainJSON bool
}

var attentionFlags attentionCmdFlags

var attentionCmd = &cobra.Command{
	Use:   "attention",
	Short: "Show which entities need the operator now (read-only, local store only)",
	Long: `The read-time attention evaluator's operator surface
(docs/behavior/pg-desk/attention.md). Both subcommands run the same pure
evaluator the pg-desk-attention plugin and the dashboard use, against the
local store only: no network, no hydration, no write.`,
}

var attentionListCmd = &cobra.Command{
	Use:   "list [--json]",
	Short: "Print every entity that needs the operator now, with its group",
	Long: `Print the evaluator's full result. With --json (or PG_DESK_OUTPUT=json) one
JSON document, schemaVersion 1: now, an optional degraded, items and groups.
Without it, one line per item in canonical order with the group label.
Exit 0 whenever the store is readable, including when nothing needs the
operator; exit 1 when the store cannot be read or the configuration cannot be
loaded or names an unknown rule kind.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error { return execAttentionList(cmd) },
}

var attentionExplainCmd = &cobra.Command{
	Use:   "explain <type>:<id> [--json]",
	Short: "Say why an entity is or is not listed as needing the operator",
	Long: `Print every candidate any rule raised for the entity and, for each, the first
suppressor that dropped it (the hidden state, the wip state, a suppress.*
annotation or a context suppressor) or that it survived; for a rule that raised
nothing, why it did not apply. Also prints the entity's group. An entity pg-desk
does not hold is not an error: it says so and exits 0. Exit 1 when the store
cannot be read, the configuration cannot be loaded, or the argument is not
shaped <type>:<id>.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error { return execAttentionExplain(cmd, args[0]) },
}

func init() {
	attentionListCmd.Flags().BoolVar(&attentionFlags.listJSON, "json", false, "Print one JSON document (schemaVersion 1)")
	attentionExplainCmd.Flags().BoolVar(&attentionFlags.explainJSON, "json", false, "Print one JSON document (schemaVersion 1)")
	attentionCmd.AddCommand(attentionListCmd, attentionExplainCmd)
	rootCmd.AddCommand(attentionCmd)
}

// evaluateAttention runs the evaluator over the read-only store. verb names
// the caller in error text. The store is returned open (the caller closes it)
// so explain can ask it whether an unevaluated entity is held.
func evaluateAttention(cmd *cobra.Command, verb string) (res attention.Result, st *store.Store, repo string, err error) {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return attention.Result{}, nil, "", fmt.Errorf("%s: load config: %w", verb, err)
	}
	if len(cfg.Repos) == 0 {
		return attention.Result{}, nil, "", fmt.Errorf("%s: no repository configured", verb)
	}
	repo = cfg.Repos[0].Remote
	st, err = deskStoreOpenReadOnly()
	if err != nil {
		return attention.Result{}, nil, "", fmt.Errorf("%s: open store: %w", verb, err)
	}
	res, err = attention.Evaluate(attention.Inputs{Store: st, Repo: repo, Config: cfg, Clock: attentionNow})
	if err != nil {
		_ = st.Close()
		return attention.Result{}, nil, "", fmt.Errorf("%s: %w", verb, err)
	}
	return res, st, repo, nil
}

func execAttentionList(cmd *cobra.Command) error {
	res, st, _, err := evaluateAttention(cmd, "attention list")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	out := cmd.OutOrStdout()
	if resolveJSONOutput(attentionFlags.listJSON) {
		return writeAttentionJSON(out, res.Document())
	}
	if res.Degraded {
		if _, err := fmt.Fprintln(out, "degraded: the store is not migrated, so suppress.* overrides are unavailable"); err != nil {
			return err
		}
	}
	if len(res.Items) == 0 {
		_, err := fmt.Fprintln(out, "nothing needs attention")
		return err
	}
	labels := map[string]string{}
	for _, g := range res.Groups {
		labels[g.Key] = g.Label
	}
	for _, it := range res.Items {
		if _, err := fmt.Fprintf(out, "%-6s %s:%s  %s  [%s]\n", it.Severity, it.Type, it.ID, it.Summary, labels[it.Group]); err != nil {
			return err
		}
	}
	return nil
}

// explainDocument is the JSON document of `attention explain --json`.
type explainDocument struct {
	SchemaVersion int    `json:"schemaVersion"`
	Now           string `json:"now"`
	Degraded      bool   `json:"degraded,omitempty"`
	Ref           string `json:"ref"`
	// Held is false for an entity pg-desk does not hold.
	Held bool `json:"held"`
	// Evaluated is false for a held entity that has no interpretation row (or
	// is inactive), so no rule ran for it.
	Evaluated  bool               `json:"evaluated"`
	Listed     bool               `json:"listed"`
	Group      string             `json:"group,omitempty"`
	Candidates []explainCandidate `json:"candidates"`
	NotRaised  []explainNotRaised `json:"notRaised"`
}

type explainCandidate struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
	// SuppressedBy is the first suppressor that dropped the candidate; absent
	// when it survived.
	SuppressedBy string `json:"suppressedBy,omitempty"`
	Survived     bool   `json:"survived"`
}

type explainNotRaised struct {
	Rule string `json:"rule"`
	Why  string `json:"why"`
}

func execAttentionExplain(cmd *cobra.Command, ref string) error {
	typ, id, ok := links.ParseRef(ref)
	if !ok {
		return fmt.Errorf("attention explain: %q is not shaped <type>:<id>", ref)
	}
	res, st, repo, err := evaluateAttention(cmd, "attention explain")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	doc := explainDocument{
		SchemaVersion: attention.SchemaVersion,
		Now:           res.Now.UTC().Format(time.RFC3339),
		Degraded:      res.Degraded,
		Ref:           attention.Ref(typ, id),
		Candidates:    []explainCandidate{},
		NotRaised:     []explainNotRaised{},
	}
	if tr, found := res.Traces[doc.Ref]; found {
		doc.Held, doc.Evaluated, doc.Group = true, true, tr.Group
		doc.Listed = len(tr.Survived) > 0
		for _, c := range tr.Survived {
			doc.Candidates = append(doc.Candidates, explainCandidate{Rule: c.Kind, Severity: string(c.Severity), Reason: c.Reason, Survived: true})
		}
		for _, d := range tr.Dropped {
			doc.Candidates = append(doc.Candidates, explainCandidate{Rule: d.Candidate.Kind, Severity: string(d.Candidate.Severity), Reason: d.Candidate.Reason, SuppressedBy: d.By})
		}
		sort.SliceStable(doc.Candidates, func(i, j int) bool { return doc.Candidates[i].Rule < doc.Candidates[j].Rule })
		for rule, why := range tr.NotRaised {
			doc.NotRaised = append(doc.NotRaised, explainNotRaised{Rule: rule, Why: why})
		}
		sort.Slice(doc.NotRaised, func(i, j int) bool { return doc.NotRaised[i].Rule < doc.NotRaised[j].Rule })
	} else {
		_, held, gerr := st.GetEntity(repo, typ, id)
		if gerr != nil {
			return fmt.Errorf("attention explain: read entity %s: %w", doc.Ref, gerr)
		}
		doc.Held = held
	}

	out := cmd.OutOrStdout()
	if resolveJSONOutput(attentionFlags.explainJSON) {
		return writeAttentionJSON(out, doc)
	}
	return writeExplainText(out, doc)
}

func writeExplainText(out io.Writer, d explainDocument) error {
	var b strings.Builder
	switch {
	case !d.Held:
		fmt.Fprintf(&b, "%s: not held by pg-desk\n", d.Ref)
	case !d.Evaluated:
		fmt.Fprintf(&b, "%s: held, but not evaluated (no interpretation row, or the entity is inactive)\n", d.Ref)
	default:
		if d.Listed {
			fmt.Fprintf(&b, "%s: listed (group %s)\n", d.Ref, d.Group)
		} else {
			fmt.Fprintf(&b, "%s: not listed\n", d.Ref)
		}
		for _, c := range d.Candidates {
			if c.Survived {
				fmt.Fprintf(&b, "  raised, survived:   %s [%s] %s\n", c.Rule, c.Severity, c.Reason)
			} else {
				fmt.Fprintf(&b, "  raised, suppressed: %s [%s] %s (suppressed by %s)\n", c.Rule, c.Severity, c.Reason, c.SuppressedBy)
			}
		}
		for _, n := range d.NotRaised {
			fmt.Fprintf(&b, "  not raised:         %s: %s\n", n.Rule, n.Why)
		}
	}
	if d.Degraded {
		b.WriteString("  degraded: the store is not migrated, so suppress.* overrides are unavailable\n")
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func writeAttentionJSON(out io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	_, err = fmt.Fprintln(out, string(b))
	return err
}

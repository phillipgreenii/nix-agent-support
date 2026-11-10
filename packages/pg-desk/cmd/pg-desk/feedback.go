package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// feedbackDisposition mirrors internal/interpret.Disposition's JSON shape
// (CommentID/Verdict/Overridden) — this packet's own copy for the same
// reason as desk.go's panel/disposition constants: feedback.go decodes the
// stored interpretation.dispositions blob without importing internal/interpret.
type feedbackDisposition struct {
	CommentID  string `json:"comment_id"`
	Verdict    string `json:"verdict"`
	Overridden bool   `json:"overridden,omitempty"`
}

// feedbackCmd is the parent for `feedback list`/`feedback set`
// [docs/behavior/pg-desk/feedback.md].
var feedbackCmd = &cobra.Command{
	Use:   "feedback",
	Short: "List or set an interpretation's feedback disposition",
}

var feedbackListCmd = &cobra.Command{
	Use:   "list <pr>",
	Short: "Print the PR's comments and threads with their current dispositions",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeedbackList(cmd, args[0])
	},
}

var feedbackSetFlags struct {
	disposition string
	actor       string
}

var feedbackSetCmd = &cobra.Command{
	Use:   "set <pr> <comment-id>",
	Short: "Record a disposition override, attributed to the caller",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFeedbackSet(cmd, args[0], args[1], feedbackSetFlags.disposition, feedbackSetFlags.actor)
	},
}

func init() {
	feedbackSetCmd.Flags().StringVar(&feedbackSetFlags.disposition, "disposition", "",
		"open|will-fix|wont-fix|no-action (required)")
	feedbackSetCmd.Flags().StringVar(&feedbackSetFlags.actor, "actor", "",
		"Attribute this override to this actor (defaults to the configured actor)")
	feedbackCmd.AddCommand(feedbackListCmd, feedbackSetCmd)
	rootCmd.AddCommand(feedbackCmd)
}

// dispositionsFromInterpretation decodes an interpretation row's
// dispositions blob (packet 5's rule-set verdict, verbatim — see
// internal/pipeline's own doc comment on why the stored value is never
// pre-merged with overrides) into the ordered []feedbackDisposition packet
// 5 wrote it as.
func dispositionsFromInterpretation(raw string) []feedbackDisposition {
	if raw == "" {
		return nil
	}
	var out []feedbackDisposition
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// applyFeedbackOverrides merges recorded per-comment annotation overrides
// onto base — internal/interpret.ApplyDispositionOverrides' own contract,
// reimplemented here rather than imported for the same reason as this
// file's feedbackDisposition type. base is never mutated.
func applyFeedbackOverrides(base []feedbackDisposition, overrides map[string]string) []feedbackDisposition {
	out := make([]feedbackDisposition, len(base))
	copy(out, base)
	for i, d := range out {
		if v, ok := overrides[d.CommentID]; ok {
			d.Verdict = v
			d.Overridden = true
			out[i] = d
		}
	}
	return out
}

func renderFeedbackList(w io.Writer, rows []feedbackDisposition) error {
	if len(rows) == 0 {
		_, err := io.WriteString(w, "(no comments)\n")
		return err
	}
	for _, d := range rows {
		overridden := ""
		if d.Overridden {
			overridden = " (overridden)"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s%s\n", d.CommentID, d.Verdict, overridden); err != nil {
			return err
		}
	}
	return nil
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// feedbackNow is the clock; overridable in tests.
var feedbackNow = func() string { return time.Now().UTC().Format(time.RFC3339) }

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
		return runFeedbackSet(cmd, args[0], args[1])
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

func runFeedbackList(cmd *cobra.Command, ref string) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("feedback list: load config: %w", err)
	}
	st, err := deskStoreOpen()
	if err != nil {
		return fmt.Errorf("feedback list: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	repo, id, err := resolvePRRef(cfg, ref)
	if err != nil {
		return fmt.Errorf("feedback list: %w", err)
	}
	interp, found, err := st.GetInterpretation(repo, entityTypePR, id)
	if err != nil {
		return fmt.Errorf("feedback list: %w", err)
	}
	if !found {
		return fmt.Errorf("feedback list: %s does not resolve", ref)
	}

	base := dispositionsFromInterpretation(interp.Dispositions)
	overrides := map[string]string{}
	for _, d := range base {
		ann, found, aerr := st.GetAnnotation(repo, entityTypePR, id, d.CommentID)
		if aerr != nil {
			return fmt.Errorf("feedback list: read annotation for comment %s: %w", d.CommentID, aerr)
		}
		if found && ann.Disposition != "" {
			overrides[d.CommentID] = ann.Disposition
		}
	}
	merged := applyFeedbackOverrides(base, overrides)
	sort.Slice(merged, func(i, j int) bool { return merged[i].CommentID < merged[j].CommentID })

	return renderFeedbackList(cmd.OutOrStdout(), merged)
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

func runFeedbackSet(cmd *cobra.Command, ref, commentID string) error {
	if !validDisposition(feedbackSetFlags.disposition) {
		return fmt.Errorf("feedback set: --disposition must be one of open|will-fix|wont-fix|no-action, got %q", feedbackSetFlags.disposition)
	}

	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("feedback set: load config: %w", err)
	}
	st, err := deskStoreOpen()
	if err != nil {
		return fmt.Errorf("feedback set: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	repo, id, err := resolvePRRef(cfg, ref)
	if err != nil {
		return fmt.Errorf("feedback set: %w", err)
	}
	interp, found, err := st.GetInterpretation(repo, entityTypePR, id)
	if err != nil {
		return fmt.Errorf("feedback set: %w", err)
	}
	if !found {
		return fmt.Errorf("feedback set: %s does not resolve", ref)
	}

	base := dispositionsFromInterpretation(interp.Dispositions)
	known := false
	for _, d := range base {
		if d.CommentID == commentID {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("feedback set: comment %q does not resolve on %s", commentID, ref)
	}

	actor := actorFor(cfg, feedbackSetFlags.actor)
	if actor == "" {
		return fmt.Errorf("feedback set: no actor available (pass --actor or set config's actor)")
	}

	a := store.Annotation{
		Repo:        repo,
		EntityType:  entityTypePR,
		EntityID:    id,
		CommentID:   commentID,
		Disposition: feedbackSetFlags.disposition,
		SetBy:       actor,
		SetAt:       feedbackNow(),
	}
	if err := st.UpsertAnnotation(a); err != nil {
		return fmt.Errorf("feedback set: %w", err)
	}
	return nil
}

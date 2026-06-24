package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/spf13/cobra"
)

// feedbackFlags holds the parsed CLI flags shared across `feedback` subcommands.
type feedbackFlags struct {
	store  string
	json   bool
	active bool
	kind   string
	action string
	note   string
	reply  string
}

var fbF feedbackFlags

// newFeedbackCmd constructs the `feedback` command tree. It is registered via
// init() so the command self-wires into rootCmd.
func newFeedbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "feedback",
		Short: "Query and disposition PR feedback from the local store",
		Long: `Inspect and act on PR feedback items stored locally by pg-pr sync / the
pg-pr daemon.

  feedback list <repo> <pr>         list feedback for a PR
  feedback show <id>                show one feedback item in full
  feedback disposition <id>         record the agent's decision on an item`,
	}

	// --store / PG_PR_STORE env override — resolved at flag-parse time.
	defaultStore := os.Getenv("PG_PR_STORE")
	if defaultStore == "" {
		defaultStore = store.DefaultPath()
	}
	cmd.PersistentFlags().StringVar(&fbF.store, "store", defaultStore,
		"Path to the pg-pr SQLite store (env: PG_PR_STORE)")

	cmd.AddCommand(newFeedbackListCmd(), newFeedbackShowCmd(), newFeedbackDispositionCmd())
	return cmd
}

// newFeedbackListCmd returns the `feedback list <repo> <pr>` sub-command.
func newFeedbackListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <repo> <pr>",
		Short: "List feedback items for a PR",
		Args:  cobra.ExactArgs(2),
		RunE:  runFeedbackList,
	}
	cmd.Flags().BoolVar(&fbF.json, "json", false, "Emit machine-readable JSON")
	cmd.Flags().BoolVar(&fbF.active, "active", false, "Show only active (non-outdated, non-resolved) items")
	cmd.Flags().StringVar(&fbF.kind, "kind", "", "Filter by kind (e.g. ci-failure, pr-comments, code-comment-thread)")
	return cmd
}

func runFeedbackList(cmd *cobra.Command, args []string) error {
	repo := args[0]
	num, err := parsePR(args[1])
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	db, err := store.Open(fbF.store)
	if err != nil {
		return fmt.Errorf("feedback list: open store: %w", err)
	}
	defer func() { _ = db.Close() }()

	pr, err := db.GetPR(ctx, repo, num)
	if err != nil {
		return fmt.Errorf("feedback list: %w", err)
	}
	if pr == nil {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "no such PR in store: %s #%d\n", repo, num)
		return err
	}

	items, err := db.ListFeedback(ctx, pr.ID, store.ListFilter{
		ActiveOnly: fbF.active,
		Kind:       fbF.kind,
	})
	if err != nil {
		return fmt.Errorf("feedback list: %w", err)
	}

	if fbF.json {
		return writeJSON(cmd.OutOrStdout(), items)
	}

	if len(items) == 0 {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "no feedback found for %s #%d\n", repo, num)
		return err
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tKIND\tSTATUS\tAUTHOR_KIND\tAGENT\tTITLE")
	for _, f := range items {
		title := f.Title
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			f.ID, f.Kind, f.Status, f.AuthorKind, f.AgentName, title)
	}
	return tw.Flush()
}

// newFeedbackShowCmd returns the `feedback show <id>` sub-command.
func newFeedbackShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a single feedback item in full",
		Args:  cobra.ExactArgs(1),
		RunE:  runFeedbackShow,
	}
	cmd.Flags().BoolVar(&fbF.json, "json", false, "Emit machine-readable JSON")
	return cmd
}

func runFeedbackShow(cmd *cobra.Command, args []string) error {
	id, err := parseFeedbackID(args[0])
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	db, err := store.Open(fbF.store)
	if err != nil {
		return fmt.Errorf("feedback show: open store: %w", err)
	}
	defer func() { _ = db.Close() }()

	f, err := db.GetFeedback(ctx, id)
	if err != nil {
		return fmt.Errorf("feedback show: %w", err)
	}
	if f == nil {
		return fmt.Errorf("feedback show: no item with id %d", id)
	}

	msgs, err := db.ListMessages(ctx, id)
	if err != nil {
		return fmt.Errorf("feedback show: list messages: %w", err)
	}

	if fbF.json {
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"feedback": f,
			"messages": msgs,
		})
	}

	w := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(w, "id:           %d\n", f.ID)
	_, _ = fmt.Fprintf(w, "kind:         %s\n", f.Kind)
	_, _ = fmt.Fprintf(w, "status:       %s\n", f.Status)
	_, _ = fmt.Fprintf(w, "author:       %s (%s)", f.AuthorLogin, f.AuthorKind)
	if f.AgentName != "" {
		_, _ = fmt.Fprintf(w, " agent=%s", f.AgentName)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "title:        %s\n", f.Title)
	if f.Body != "" {
		_, _ = fmt.Fprintf(w, "body:\n%s\n", f.Body)
	}
	if f.DispositionAction != "" {
		_, _ = fmt.Fprintf(w, "disposition:  %s", f.DispositionAction)
		if f.DispositionNote != "" {
			_, _ = fmt.Fprintf(w, " — %s", f.DispositionNote)
		}
		_, _ = fmt.Fprintln(w)
	}
	if f.ReplyBody != "" {
		_, _ = fmt.Fprintf(w, "reply:        %s\n", f.ReplyBody)
	}
	if len(msgs) > 0 {
		_, _ = fmt.Fprintf(w, "messages (%d):\n", len(msgs))
		for _, m := range msgs {
			_, _ = fmt.Fprintf(w, "  [%d] %s (%s): %s\n", m.ID, m.AuthorLogin, m.AuthorKind, m.Body)
		}
	}
	return nil
}

// newFeedbackDispositionCmd returns the `feedback disposition <id>` sub-command.
func newFeedbackDispositionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disposition <id>",
		Short: "Record the agent's decision on a feedback item",
		Long: `Record a disposition (will-fix, wont-fix, or no-action) on a feedback item.
If --reply is provided, the reply text is queued for posting on the next
pg-pr sync or daemon cycle.`,
		Args: cobra.ExactArgs(1),
		RunE: runFeedbackDisposition,
	}
	cmd.Flags().StringVar(&fbF.action, "action", "", "Disposition action: will-fix, wont-fix, or no-action (required)")
	cmd.Flags().StringVar(&fbF.note, "note", "", "Short rationale note (optional)")
	cmd.Flags().StringVar(&fbF.reply, "reply", "", "Reply text to queue for posting (optional)")
	_ = cmd.MarkFlagRequired("action")
	return cmd
}

func runFeedbackDisposition(cmd *cobra.Command, args []string) error {
	id, err := parseFeedbackID(args[0])
	if err != nil {
		return err
	}
	// CLI-level validation so the error message names the allowed values clearly.
	switch fbF.action {
	case "will-fix", "wont-fix", "no-action":
		// valid
	default:
		return fmt.Errorf("invalid --action %q: must be one of will-fix, wont-fix, no-action", fbF.action)
	}

	ctx := cmd.Context()

	db, err := store.Open(fbF.store)
	if err != nil {
		return fmt.Errorf("feedback disposition: open store: %w", err)
	}
	defer func() { _ = db.Close() }()

	payload, _ := json.Marshal(map[string]any{"feedback_id": id})
	if err := db.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.SetDisposition(id, fbF.action, fbF.note, fbF.reply); err != nil {
			return err
		}
		return tx.EnqueueEvent(store.EventFeedbackDisposed, payload)
	}); err != nil {
		return fmt.Errorf("feedback disposition: %w", err)
	}

	w := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(w, "ok feedback %d dispositioned as %s\n", id, fbF.action)
	if fbF.reply != "" {
		_, _ = fmt.Fprintln(w, "reply queued — will be posted on the next pg-pr sync / daemon cycle")
	}
	return nil
}

// parseFeedbackID converts a string argument to a positive int64 feedback id.
func parseFeedbackID(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid feedback id %q: expected a positive integer", s)
	}
	return n, nil
}

func init() {
	rootCmd.AddCommand(newFeedbackCmd())
}

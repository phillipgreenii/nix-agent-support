package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
)

// changesNow is the clock a changes call stamps seen_at and prunes with;
// tests replace it.
var changesNow = func() time.Time { return time.Now().UTC() }

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newChangesCmd(t))
	}
}

type changesFlags struct {
	consumer string
	query    string
	cached   bool
	reset    bool
	limit    int
	jsonOut  bool
}

// newChangesCmd builds `pg-desk <type> changes --consumer NAME [--query Q]
// [--cached] [--reset] [--limit N]`: the list-and-diff change feed [design
// 6.2, 9.2]. It is a typed verb with no old-schema counterpart, so it
// refuses an old-schema store.
func newChangesCmd(entityType string) *cobra.Command {
	var f changesFlags
	c := &cobra.Command{
		Use:   "changes",
		Short: fmt.Sprintf("List-and-diff %s changes through pg-connector, then list the change records past a consumer's cursor", entityType),
		Long: fmt.Sprintf(`List-and-diff (default): for each watched query of the type, run
pg-connector %[1]s list --fingerprints (no cursor: the whole current listing),
compare each listed entity's fingerprint with the one stored at its last
hydration, hydrate only the entities that are new or differ (within
hydration.max_per_poll), classify and log the change, then print the
pg-desk.changes/v1 envelope of the records past NAME's cursor and advance that
cursor once the output is flushed. A hydration that fails writes nothing, so
the entity is simply found different again on the next call: there is no retry
queue and no give-up.

A plain call (without --cached) advances NAME's own cursor exactly like a real
pg-router poll: to inspect without moving anything use --cached or
"pg-desk %[1]s history <id>".

Entity types whose list cannot be fingerprinted (thread) are refused.`, entityType),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChanges(cmd, entityType, f)
		},
	}
	c.Flags().StringVar(&f.consumer, "consumer", "", "Consumer name whose cursor this call reads and advances (required)")
	c.Flags().StringVar(&f.query, "query", "", "Restrict the call to one configured watched query")
	c.Flags().BoolVar(&f.cached, "cached", false, "Peek at the records a real call would return: no pg-connector call, no hydration, no cursor advance")
	c.Flags().BoolVar(&f.reset, "reset", false, "Replay every active entity to the consumer as reconcile")
	c.Flags().IntVar(&f.limit, "limit", 0, "Return at most N records (0 = all)")
	c.Flags().BoolVar(&f.jsonOut, "json", false, "Print the envelope as JSON")
	_ = c.MarkFlagRequired("consumer")
	return c
}

func runChanges(cmd *cobra.Command, entityType string, f changesFlags) error {
	if strings.TrimSpace(f.consumer) == "" {
		return errors.New("changes: --consumer must not be empty")
	}
	if f.limit < 0 {
		return fmt.Errorf("changes: --limit must be >= 0, got %d", f.limit)
	}
	if f.cached && f.reset {
		return errors.New("changes: --reset cannot be combined with --cached")
	}
	if !changes.FingerprintSupported(entityType) {
		cmd.SilenceUsage = true
		return fmt.Errorf("%w: pg-desk %s changes is not available because a %s list cannot be fingerprinted", changes.ErrUnsupportedType, entityType, entityType)
	}
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("changes: load config: %w", err)
	}
	st, err := openNewSchemaStore("changes")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	// --query must name a configured watched query; a real call also needs
	// at least one watched query [design 9.10]. --cached is unaffected by a
	// missing watch key.
	if !f.cached || (f.query != "" && len(cfg.WatchQueries(entityType)) > 0) {
		if _, err := changes.SelectQueries(cfg, entityType, f.query); err != nil {
			return err
		}
	}
	cmd.SilenceUsage = true

	eng := &changes.Engine{
		Cfg:      cfg,
		Store:    st,
		Lister:   gather.NewGatherer(cfg, st),
		Hydrator: pipeline.New(cfg, st, pipeline.WithLogWriter(cmd.ErrOrStderr())),
		Now:      changesNow,
		Warn:     cmd.ErrOrStderr(),
	}
	jsonOut := resolveJSONOutput(f.jsonOut)
	out, err := eng.Run(cmd.Context(), changes.Options{
		EntityType: entityType, Consumer: f.consumer, Query: f.query,
		Cached: f.cached, Reset: f.reset, Limit: f.limit,
	}, func(env changes.Envelope) error {
		if jsonOut {
			return writeChangesJSON(cmd.OutOrStdout(), env)
		}
		return renderChanges(cmd.OutOrStdout(), env)
	})
	switch {
	case errors.Is(err, changes.ErrTotalFailure):
		return newExitError(exitTotal, err)
	case err != nil:
		return err
	case out.Partial:
		return newExitError(exitPartial, errors.New("changes: partial: at least one watched query or hydration failed or degraded (see the envelope's sources)"))
	}
	return nil
}

func writeChangesJSON(w io.Writer, env changes.Envelope) error {
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("changes: marshal json: %w", err)
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// renderChanges prints one line per record, the history layout prefixed by
// the entity — <type> <id>  <seq>  <at>  <kinds>  origin=<origin> — then one
// line per watched query and the cursor movement.
func renderChanges(w io.Writer, env changes.Envelope) error {
	var b strings.Builder
	for _, r := range env.Records {
		fmt.Fprintf(&b, "%s %s  %d  %s  %s  origin=%s\n", r.Type, r.ID, r.Seq, r.At, strings.Join(r.Kinds, ", "), r.Origin)
	}
	for _, s := range env.Sources {
		fmt.Fprintf(&b, "source %s: %s", s.Query, s.Status)
		if s.Reason != "" {
			fmt.Fprintf(&b, " (%s)", s.Reason)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "cursor %d -> %d\n", env.Cursor.From, env.Cursor.To)
	_, err := io.WriteString(w, b.String())
	return err
}

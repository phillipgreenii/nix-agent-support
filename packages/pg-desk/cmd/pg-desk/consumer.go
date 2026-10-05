package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// consumerNow is the clock for consumer staleness; tests replace it.
var consumerNow = func() time.Time { return time.Now().UTC() }

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newConsumerCmd(t))
	}
}

// newConsumerCmd builds the `pg-desk <type> consumer` group with its list
// and forget verbs [design 6.9].
func newConsumerCmd(entityType string) *cobra.Command {
	g := &cobra.Command{
		Use:   "consumer",
		Short: fmt.Sprintf("List or forget the registered %s change consumers", entityType),
	}
	g.AddCommand(newConsumerListCmd(entityType), newConsumerForgetCmd(entityType))
	return g
}

func newConsumerListCmd(entityType string) *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "list",
		Short: fmt.Sprintf("List the registered %s consumers with cursor, seen_at and staleness", entityType),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsumerList(cmd, entityType, resolveJSONOutput(jsonOut))
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, `Print {"consumers": [...]} instead of one line per consumer`)
	return c
}

func newConsumerForgetCmd(entityType string) *cobra.Command {
	return &cobra.Command{
		Use:   "forget <name>",
		Short: fmt.Sprintf("Delete a %s consumer's registration so it stops holding back change_log pruning", entityType),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConsumerForget(cmd, entityType, args[0])
		},
	}
}

// consumerView is one consumer row as printed. Stale means the consumer was
// not seen within consumer_stale_after (or never), so change_log pruning no
// longer waits for it.
type consumerView struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Cursor int64  `json:"cursor"`
	SeenAt string `json:"seen_at"`
	Stale  bool   `json:"stale"`
}

type consumerListPayload struct {
	Consumers []consumerView `json:"consumers"`
}

func runConsumerList(cmd *cobra.Command, entityType string, jsonOut bool) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("consumer list: load config: %w", err)
	}
	st, err := openNewSchemaStore("consumer list")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	rows, err := st.ListConsumers()
	if err != nil {
		return fmt.Errorf("consumer list: %w", err)
	}
	staleAfter := cfg.ConsumerStaleAfter()
	if staleAfter <= 0 {
		staleAfter = store.DefaultConsumerStaleAfter
	}
	now := consumerNow()
	views := []consumerView{}
	for _, c := range rows {
		if c.Type != entityType {
			continue
		}
		views = append(views, consumerView{
			Name: c.Name, Type: c.Type, Cursor: c.Cursor, SeenAt: c.SeenAt,
			Stale: consumerIsStale(c.SeenAt, now, staleAfter),
		})
	}
	if jsonOut {
		b, err := json.MarshalIndent(consumerListPayload{Consumers: views}, "", "  ")
		if err != nil {
			return fmt.Errorf("consumer list: marshal json: %w", err)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(b))
		return err
	}
	return renderConsumers(cmd.OutOrStdout(), views)
}

// consumerIsStale mirrors PruneChangeLog's rule: a consumer never seen, or
// not seen since now-staleAfter, is stale.
func consumerIsStale(seenAt string, now time.Time, staleAfter time.Duration) bool {
	if seenAt == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, seenAt)
	if err != nil {
		return true
	}
	return t.Before(now.Add(-staleAfter))
}

// renderConsumers prints one line per consumer:
// <name>  cursor=<n>  seen_at=<at|never>  <fresh|stale>.
func renderConsumers(w io.Writer, views []consumerView) error {
	for _, v := range views {
		seen := v.SeenAt
		if seen == "" {
			seen = "never"
		}
		state := "fresh"
		if v.Stale {
			state = "stale"
		}
		if _, err := fmt.Fprintf(w, "%s  cursor=%d  seen_at=%s  %s\n", v.Name, v.Cursor, seen, state); err != nil {
			return err
		}
	}
	return nil
}

func runConsumerForget(cmd *cobra.Command, entityType, name string) error {
	if _, err := deskConfigLoad(cmd.Context()); err != nil {
		return fmt.Errorf("consumer forget: load config: %w", err)
	}
	st, err := openNewSchemaStore("consumer forget")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	rows, err := st.ListConsumers()
	if err != nil {
		return fmt.Errorf("consumer forget: %w", err)
	}
	registered := false
	for _, c := range rows {
		if c.Name == name && c.Type == entityType {
			registered = true
			break
		}
	}
	if err := st.ForgetConsumer(name, entityType); err != nil {
		return fmt.Errorf("consumer forget: %w", err)
	}
	msg := fmt.Sprintf("forgot %s consumer %s", entityType, name)
	if !registered {
		msg = fmt.Sprintf("no %s consumer %s was registered; nothing to forget", entityType, name)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), msg)
	return err
}

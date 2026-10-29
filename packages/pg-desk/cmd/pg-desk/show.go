package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// showCmdFlags is a named type (rather than an inline anonymous struct) so
// tests can build and pass a value of this exact type without repeating the
// field list at every call site.
type showCmdFlags struct {
	jsonOut bool
}

var showFlags showCmdFlags

// showCmd implements `pg-desk show <pr>`
// [docs/behavior/pg-desk/operator-commands.md]. Exit codes: 0 on success; 1
// when <pr> does not resolve, or the store cannot be read.
var showCmd = &cobra.Command{
	Use:   "show <pr>",
	Short: "Print a PR's stored interpretation",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runShow(cmd, args[0])
	},
}

func init() {
	showCmd.Flags().BoolVar(&showFlags.jsonOut, "json", false,
		"Print a machine-readable JSON payload instead of the human table")
	rootCmd.AddCommand(showCmd)
}

// showPayload is show --json's machine-readable payload. It MUST carry at
// least wip (this packet's own Acceptance Criteria) — hidden/wip are read
// straight from the annotation table, joined here at read time, exactly as
// the design's "hidden and WIP are not interpreted" rule requires for every
// other read path (open, serve).
type showPayload struct {
	Repo           string `json:"repo"`
	EntityType     string `json:"entity_type"`
	EntityID       string `json:"entity_id"`
	Ownership      string `json:"ownership"`
	Category       string `json:"category"`
	Panel          string `json:"panel"`
	GateState      string `json:"gate_state,omitempty"`
	ReadyToPromote bool   `json:"ready_to_promote"`
	Degraded       bool   `json:"degraded"`
	AsOf           string `json:"as_of"`
	Hidden         bool   `json:"hidden"`
	HiddenReason   string `json:"hidden_reason,omitempty"`
	WIP            bool   `json:"wip"`
}

func runShow(cmd *cobra.Command, ref string) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("show: load config: %w", err)
	}

	repo, id, err := resolvePRRef(cfg, ref)
	if err != nil {
		return fmt.Errorf("show: %w", err)
	}

	st, err := openDeskStore()
	if err != nil {
		return fmt.Errorf("show: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	interp, found, err := st.GetInterpretation(repo, entityTypePR, id)
	if err != nil {
		return fmt.Errorf("show: %w", err)
	}
	if !found {
		return fmt.Errorf("show: %s does not resolve", ref)
	}

	hidden, hiddenReason, wip := false, "", false
	if ann, found, aerr := st.GetPRAnnotation(repo, entityTypePR, id); aerr != nil {
		return fmt.Errorf("show: read annotation: %w", aerr)
	} else if found {
		if ann.Hidden != nil {
			hidden = *ann.Hidden
		}
		hiddenReason = ann.HiddenReason
		if ann.WIP != nil {
			wip = *ann.WIP
		}
	}

	payload := showPayload{
		Repo:           repo,
		EntityType:     entityTypePR,
		EntityID:       id,
		Ownership:      interp.Ownership,
		Category:       interp.Category,
		Panel:          interp.Panel,
		GateState:      interp.GateState,
		ReadyToPromote: interp.ReadyToPromote,
		Degraded:       interp.Degraded,
		AsOf:           interp.AsOf,
		Hidden:         hidden,
		HiddenReason:   hiddenReason,
		WIP:            wip,
	}

	if resolveJSONOutput(showFlags.jsonOut) {
		return writeShowJSON(cmd.OutOrStdout(), payload)
	}
	return renderShow(cmd.OutOrStdout(), payload)
}

func writeShowJSON(w io.Writer, p showPayload) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("show: marshal json: %w", err)
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func renderShow(w io.Writer, p showPayload) error {
	// p.EntityID already carries the full qualified "owner/repo#N" form
	// (resolvePRRef in desk.go always rebuilds it as repo+"#"+n), so it is
	// printed here as-is — prepending p.Repo+"#" on top of it would
	// double-print the repo prefix (pg2-5mnbo), unlike writeShowJSON which
	// already serializes EntityID verbatim and was never affected.
	if _, err := fmt.Fprintf(
		w,
		"%s\townership=%s\tcategory=%s\tpanel=%s\tready_to_promote=%v\tdegraded=%v\tas_of=%s\thidden=%v\twip=%v\n",
		p.EntityID, orDash(p.Ownership), orDash(p.Category), orDash(p.Panel),
		p.ReadyToPromote, p.Degraded, orDash(p.AsOf), p.Hidden, p.WIP,
	); err != nil {
		return err
	}
	return nil
}

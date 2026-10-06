package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report/internal/store"
)

func init() { registerCommand(newStatusCmd) }

// lastOutcomeJSON is a source's most recent pull outcome. Reason is empty when
// the pull gave none; At is null when the source has never been pulled.
type lastOutcomeJSON struct {
	Status string  `json:"status"`
	Reason string  `json:"reason"`
	At     *string `json:"at"`
}

type sourceStatusJSON struct {
	Source           string          `json:"source"`
	LastOutcome      lastOutcomeJSON `json:"last_outcome"`
	LastSuccessAt    *string         `json:"last_success_at"`
	EntryCount       int             `json:"entry_count"`
	NewestOccurredAt *string         `json:"newest_occurred_at"`
}

type lastReportJSON struct {
	GeneratedAt string  `json:"generated_at"`
	Kind        string  `json:"kind"`
	Since       *string `json:"since"`
	Before      *string `json:"before"`
}

type statusJSON struct {
	Sources        []sourceStatusJSON `json:"sources"`
	StoreSizeBytes int64              `json:"store_size_bytes"`
	LastReport     *lastReportJSON    `json:"last_report"`
}

// timePtr renders t as RFC3339 UTC; the zero time (none yet, or an open-ended
// range bound) is nil, which encodes as null.
func timePtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func buildStatus(d store.StatusData) statusJSON {
	out := statusJSON{
		Sources:        make([]sourceStatusJSON, 0, len(d.Sources)),
		StoreSizeBytes: d.StoreSizeBytes,
	}
	for _, s := range d.Sources {
		out.Sources = append(out.Sources, sourceStatusJSON{
			Source:           s.Source,
			LastOutcome:      lastOutcomeJSON{Status: s.LastStatus, Reason: s.LastReason, At: timePtr(s.LastAt)},
			LastSuccessAt:    timePtr(s.LastSuccessAt),
			EntryCount:       s.EntryCount,
			NewestOccurredAt: timePtr(s.NewestOccurredAt),
		})
	}
	if r := d.LastReport; r != nil {
		out.LastReport = &lastReportJSON{
			GeneratedAt: r.GeneratedAt.UTC().Format(time.RFC3339),
			Kind:        r.Kind,
			Since:       timePtr(r.Since),
			Before:      timePtr(r.Before),
		}
	}
	return out
}

// newStatusCmd builds `status`: per source, the last pull outcome, last
// success time, entry count and newest occurred_at, plus store size and the
// last report. It reads the store only and never execs pg-connector.
//
// Status is a health read, not a gate: it exits 0 whatever the sources' state,
// including on an empty store. It still fails (non-zero) when it cannot run at
// all: an unsupported --output, an unreadable config, or an unopenable store.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show per-source pull health, store size and the last report",
		Long: "Prints, per source, the last pull outcome, the last success time, the entry " +
			"count and the newest occurred_at; plus the store size and the last report. " +
			"It answers \"why is Tuesday empty\". It exits 0 regardless of source health.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, rawOut, storeFlag := globalFlags(cmd)
			format, err := outputFormat(rawOut)
			if err != nil {
				return err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			loc, err := cfg.Location()
			if err != nil {
				return err
			}
			st, err := store.Open(config.StorePath(cfg, storeFlag))
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			data, err := st.Status(cmd.Context())
			if err != nil {
				return err
			}
			v := buildStatus(data)
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), v)
			}
			return writeStatusHuman(cmd.OutOrStdout(), data, v, loc)
		},
	}
}

func writeStatusHuman(w io.Writer, d store.StatusData, v statusJSON, loc *time.Location) error {
	at := func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		return t.In(loc).Format("2006-01-02 15:04 MST")
	}
	var b strings.Builder
	if len(d.Sources) == 0 {
		b.WriteString("sources: none yet (no pulls recorded and no entries stored)\n")
	} else {
		b.WriteString("sources:\n")
		for _, s := range d.Sources {
			fmt.Fprintf(&b, "  %s\n", s.Source)
			outcome := s.LastStatus
			if outcome == "" {
				outcome = "never pulled"
			}
			if s.LastReason != "" {
				outcome += " (" + s.LastReason + ")"
			}
			fmt.Fprintf(&b, "    last outcome:       %s at %s\n", outcome, at(s.LastAt))
			fmt.Fprintf(&b, "    last success:       %s\n", at(s.LastSuccessAt))
			fmt.Fprintf(&b, "    entries:            %d\n", s.EntryCount)
			fmt.Fprintf(&b, "    newest occurred_at: %s\n", at(s.NewestOccurredAt))
		}
	}
	fmt.Fprintf(&b, "store size: %d bytes\n", v.StoreSizeBytes)
	if r := d.LastReport; r == nil {
		b.WriteString("last report: none\n")
	} else {
		rng := func(t time.Time) string {
			if t.IsZero() {
				return "open"
			}
			return at(t)
		}
		fmt.Fprintf(&b, "last report: %s generated %s (since %s, before %s)\n",
			r.Kind, at(r.GeneratedAt), rng(r.Since), rng(r.Before))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

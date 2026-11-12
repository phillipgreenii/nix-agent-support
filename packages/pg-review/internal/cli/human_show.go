package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/render"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/validate"
)

func init() { registerCommand(groupHuman, newHumanShowCmd) }

func newHumanShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>...",
		Short: "Show one or more review items (records a view of each)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			withEvidence, _ := cmd.Flags().GetBool("evidence")
			ctx := cmd.Context()
			l, err := ledger.Open(ctx)
			if err != nil {
				return mapHumanError(err)
			}
			defer func() { _ = l.Close() }()
			items, err := l.ShowItems(ctx, ids)
			if err != nil {
				return mapHumanError(err)
			}
			counts, err := l.Counts(ctx)
			if err != nil {
				return mapHumanError(err)
			}
			format, err := OutputFormat(cmd)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var docs []map[string]any
			for i, it := range items {
				text := shownText(it, counts.Pending)
				ev, evPath := "", ""
				if withEvidence {
					ev, evPath = evidenceText(ctx, l, it)
				}
				if format == outputJSON {
					d := map[string]any{"id": it.Number, "state": it.State, "text": text}
					if withEvidence {
						d["evidence"], d["evidence_path"] = ev, evPath
					}
					docs = append(docs, d)
					continue
				}
				if i > 0 {
					fmt.Fprint(out, "\n----------------------------------------\n\n")
				}
				fmt.Fprint(out, text)
				if withEvidence {
					fmt.Fprintf(out, "\nEvidence path: %s\n%s", render.Strip(evPath), ev)
				}
			}
			if format == outputJSON {
				b, _ := json.Marshal(docs)
				fmt.Fprintln(out, string(b))
			}
			return nil
		},
	}
	cmd.Flags().Bool("evidence", false, "include the evidence behind each item")
	return cmd
}

// evidenceText prints the evidence rows the write-up cites, from the run's
// evidence directory. The directory expires after the evidence TTL.
func evidenceText(ctx context.Context, l *ledger.Ledger, it ledger.Item) (text, path string) {
	run, err := l.GetRun(ctx, it.RunID)
	if err != nil {
		return "(the run of this item is not in the ledger)\n", ""
	}
	path = run.EvidenceDir
	if _, err := os.Stat(path); err != nil {
		return "(the evidence directory has expired)\n", path
	}
	in, err := validate.Load(run.EvidenceDir, run.StagingPath)
	if err != nil {
		return fmt.Sprintf("(the evidence could not be read: %v)\n", err), path
	}
	cited := map[string]bool{}
	for _, id := range it.Writeup.Evidence {
		cited[id] = true
	}
	for _, r := range in.Evidence {
		if !cited[r.ID] {
			continue
		}
		b, _ := json.Marshal(r)
		text += render.Strip(string(b)) + "\n"
	}
	if text == "" {
		text = "(no cited evidence row was found)\n"
	}
	return text, path
}

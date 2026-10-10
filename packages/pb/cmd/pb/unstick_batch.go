package main

import (
	"fmt"
	"sort"

	"github.com/phillipgreenii/pb/internal/unstick"
	"github.com/spf13/cobra"
)

type batchResult struct {
	Name  string   `json:"name"`
	Size  int      `json:"size"`
	IDs   []string `json:"ids"`
	Batch string   `json:"batch_file"`
	Facts string   `json:"facts_file"`
}

func newUnstickBatchCmd(_ unstickEnv) *cobra.Command {
	var (
		workdir, name string
		ids           []string
		asJSON        bool
	)
	cmd := &cobra.Command{
		Use:   "batch",
		Short: "Write a follow-up batch (batches/<name> and facts/<name>.json) from the work directory's export",
		Long: `Stage 7 of /pb:unstick-beads: builds an extra batch for beads that need a second
look (for example follow-ups discovered by a worker). Reads <workdir>/export.jsonl
and writes <workdir>/batches/<name> plus <workdir>/facts/<name>.json. Every id
MUST be present in the export; --ids is a comma-separated list.

Exit codes: 0 ok; 1 usage, IO or unknown-id error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w, err := requireWorkdir(workdir)
			if err != nil {
				return err
			}
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if len(ids) == 0 {
				return fmt.Errorf("--ids is required (comma-separated bead ids)")
			}
			rows, err := unstick.ReadExportFile(w.Join(unstick.ExportFile))
			if err != nil {
				return err
			}
			if err := unstick.WriteBatch(w, name, ids, unstick.NewGraph(rows)); err != nil {
				return err
			}
			sorted := append([]string(nil), ids...)
			sort.Strings(sorted)
			res := batchResult{
				Name: name, Size: len(sorted), IDs: sorted,
				Batch: w.Join(unstick.BatchesDir, name), Facts: w.Join(unstick.FactsDir, name+".json"),
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "batch %s: %d bead(s)\n  %s\n  %s\n", res.Name, res.Size, res.Batch, res.Facts)
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "absolute path of the sweep work directory (required)")
	cmd.Flags().StringVar(&name, "name", "", "batch name, e.g. FOLLOWUPS (required)")
	cmd.Flags().StringSliceVar(&ids, "ids", nil, "comma-separated bead ids (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

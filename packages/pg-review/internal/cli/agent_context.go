package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

func init() { registerCommand(groupAgent, newAgentContextCmd) }

// ContextTopic is one known topic returned to the analyst. It is data, never
// an instruction.
type ContextTopic struct {
	TopicID    string `json:"topic_id"`
	SubjectKey string `json:"subject_key"`
	DefinedIn  string `json:"defined_in"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
}

// Context is what `agent context` prints.
type Context struct {
	Run             int64          `json:"run"`
	Lens            string         `json:"lens"`
	Scope           string         `json:"scope"`
	Window          Window         `json:"window"`
	EvidenceDir     string         `json:"evidence_dir"`
	EvidencePath    string         `json:"evidence"`
	QueryLogPath    string         `json:"query_log"`
	QueryResultsDir string         `json:"query_results"`
	StagingPath     string         `json:"staging_path"`
	Topics          []ContextTopic `json:"topics"`
}

// Window is the run's time window.
type Window struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func newAgentContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Print the run context as JSON: lens, scope, window, known topics and evidence paths",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runID, err := resolveRun(cmd)
			if err != nil {
				return err
			}
			l, err := ledger.Open(cmd.Context())
			if err != nil {
				return err
			}
			defer l.Close()
			run, err := l.GetRun(cmd.Context(), runID)
			if err != nil {
				return mapFileError(err)
			}
			tps, err := l.TopicsForLens(cmd.Context(), run.Lens)
			if err != nil {
				return err
			}
			c := Context{
				Run: run.ID, Lens: run.Lens, Scope: run.Scope,
				Window:      Window{From: run.WindowFrom, To: run.WindowTo},
				EvidenceDir: run.EvidenceDir, StagingPath: run.StagingPath,
				EvidencePath:    filepath.Join(run.EvidenceDir, "evidence.json"),
				QueryLogPath:    filepath.Join(run.EvidenceDir, "query-log.jsonl"),
				QueryResultsDir: filepath.Join(run.EvidenceDir, "query-results"),
				Topics:          []ContextTopic{},
			}
			for _, tp := range tps {
				c.Topics = append(c.Topics, ContextTopic{TopicID: tp.ID, SubjectKey: tp.SubjectKey, DefinedIn: tp.DefinedIn, Kind: tp.Kind, State: tp.State})
			}
			b, err := json.Marshal(c)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(b))
			return nil
		},
	}
	cmd.Flags().String("run", "", "run id (default: the PG_REVIEW_RUN environment variable)")
	return cmd
}

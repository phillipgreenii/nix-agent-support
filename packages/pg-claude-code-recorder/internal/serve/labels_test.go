package serve_test

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
)

// The daemon consumer of the label table: raw inputs (registry files, history
// lines, exits, restarts, end files) go through a real daemon, and the evidence
// it returns for the row's Conversation must be the row's. The daemon never
// returns a label; the client test turns the same evidence into one.
func TestLabelTableInputsProduceTheRowsEvidence(t *testing.T) {
	var loaded []string
	for _, row := range clienttest.LabelRows(t) {
		loaded = append(loaded, row.Name)
		t.Run(row.Name, func(t *testing.T) {
			e := clienttest.RunRow(t, row)
			got, err := e.Dial(context.Background()).Conversation(context.Background(), row.Session)
			if err != nil {
				t.Fatalf("conversation.show %s: %v", row.Session, err)
			}
			want := row.Evidence.Evidence()
			if got.Ended != want.Ended || got.RegistryPresentAtExit != want.RegistryPresentAtExit ||
				got.ExitObserved != want.ExitObserved || got.ClearEntry != want.ClearEntry ||
				got.ExitEntry != want.ExitEntry || got.RegistryStaleAtDiscovery != want.RegistryStaleAtDiscovery {
				t.Fatalf("evidence %+v, want %+v", got.Evidence, want)
			}
		})
	}
	clienttest.Covered(t, "daemon", loaded)
}

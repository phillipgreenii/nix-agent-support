//go:build integration

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client/clienttest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lifecycle"
)

func conversationRows(t *testing.T, e *clienttest.Env, args ...string) ([]lifecycle.Ended, int) {
	t.Helper()
	a, out, errw := testApp(nil)
	code := a.run(append([]string{"conversations", "--socket", e.Socket, "--json", "--since", "2000-01-01T00:00:00Z"}, args...))
	if code != 0 && code != 5 {
		t.Fatalf("conversations %v: exit %d, %s", args, code, errw.String())
	}
	var rows []lifecycle.Ended
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("conversations %v: %v: %q", args, err, out.String())
	}
	return rows, code
}

func listed(rows []lifecycle.Ended, session string) bool {
	for _, r := range rows {
		if r.SessionID == session {
			return true
		}
	}
	return false
}

// The command consumer of the label table: each row goes through a real daemon
// and the conversations subcommand, which derives the label with the client's
// EndLabel.
func TestLabelTableRowsThroughConversationsEnded(t *testing.T) {
	var loaded []string
	for _, row := range clienttest.LabelRows(t) {
		loaded = append(loaded, row.Name)
		t.Run(row.Name, func(t *testing.T) {
			e := clienttest.RunRow(t, row)
			for _, label := range []string{"crashed", "exited", "cleared", "closed", "unknown"} {
				rows, _ := conversationRows(t, e, "--ended", label)
				if got, want := listed(rows, row.Session), row.Label == label; got != want {
					t.Errorf("--ended %s lists %s = %v, want %v", label, row.Session, got, want)
				}
				for _, r := range rows {
					if string(r.Label) != label {
						t.Errorf("--ended %s returned a %s row", label, r.Label)
					}
				}
			}
			cr, _ := conversationRows(t, e, "--crashed")
			if got, want := listed(cr, row.Session), row.Label == "crashed"; got != want {
				t.Errorf("--crashed lists %s = %v, want %v", row.Session, got, want)
			}
			su, _ := conversationRows(t, e, "--suspected-crash")
			if got := listed(su, row.Session); got != row.Suspected {
				t.Errorf("--suspected-crash lists %s = %v, want %v", row.Session, got, row.Suspected)
			}
			for _, r := range su {
				if r.Label != lifecycle.LabelCrashed && (r.Label != lifecycle.LabelUnknown || !r.RegistryStaleAtDiscovery) {
					t.Errorf("--suspected-crash listed a %s row without the stale fact", r.Label)
				}
			}
			if strings.Contains(row.Name, "closed") {
				if rows, _ := conversationRows(t, e, "--ended", "closed normally"); !listed(rows, row.Session) && row.Label == "closed" {
					t.Errorf("--ended 'closed normally' does not list a closed row")
				}
			}
		})
	}
	clienttest.Covered(t, "cmd", loaded)
}

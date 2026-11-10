package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// issueShowScript is a fake pg-connector that answers `issue show <key>` with
// a Jira-shaped issue carrying the connector's operator facts, and exits 4
// (not_found) for any other key.
func issueShowScript(key, state, changedAt, operatorAt string) string {
	return fmt.Sprintf(`
case "$1 $2 $3" in
  "issue show %[1]s")
    printf '%%s' '{"protocolVersion":1,"schemaVersion":7,"result":{"id":"%[1]s","title":"t","state":"%[2]s","assignee":"Someone","owner":"","issue_type":"Task","as_of":"2026-10-06T00:00:00Z","status_changed_at":"%[3]s","operator_updated_at":"%[4]s"}}'
    exit 0;;
esac
exit 4`, key, state, changedAt, operatorAt)
}

// newRunStore opens a fresh OLD-schema store (the live store is on that
// version until the cutover) at a temp path and points `run` at it. Each `run`
// opens, and closes, its own handle; st is for seeding, and callers inspect the
// result through reopen.
func newRunStore(t *testing.T, cfg *config.Config) (st *store.Store, reopen func() *store.Store) {
	t.Helper()
	origConfigLoad, origStoreOpen, origChange := runConfigLoad, runStoreOpen, runF.change
	t.Cleanup(func() { runConfigLoad, runStoreOpen, runF.change = origConfigLoad, origStoreOpen, origChange })
	runConfigLoad = func(context.Context) (*config.Config, error) { return cfg, nil }

	dbPath := filepath.Join(t.TempDir(), "test.db")
	store.SetSynchronousForTests("OFF")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runStoreOpen = func() (*store.Store, error) { return store.Open(dbPath) }
	return st, func() *store.Store {
		t.Helper()
		v, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("reopen test store: %v", err)
		}
		t.Cleanup(func() { _ = v.Close() })
		return v
	}
}

// runIssue drives `pg-desk run issue <key>` with the given change kind (the
// flag is a package var, and RunE does not parse flags).
func runIssue(t *testing.T, key, change string) error {
	t.Helper()
	cmd, _, err := rootCmd.Find([]string{"run"})
	if err != nil {
		t.Fatalf("rootCmd has no run subcommand: %v", err)
	}
	cmd.SetContext(context.Background())
	runF.change = change
	return cmd.RunE(cmd, []string{"issue", key})
}

// runIssueForTest runs `run issue` against a fresh store and returns a
// reopened store for inspection.
func runIssueForTest(t *testing.T, cfg *config.Config, key, change string) (*store.Store, error) {
	t.Helper()
	_, reopen := newRunStore(t, cfg)
	err := runIssue(t, key, change)
	return reopen(), err
}

func watchedJiraCfg() *config.Config {
	cfg := jiraTestCfg()
	cfg.Watch.Issue.Queries = []string{"assigned-to-me"}
	return cfg
}

// With watch.issue.queries configured, `run issue <key>` stores the issue as an
// issue entity carrying the operator facts, and the attention projection reads
// them with no PR interpretation row involved.
func TestRunCmdIssue_Jira_HydratesIssueEntityWhenWatched(t *testing.T) {
	installFakePGConnector(t, issueShowScript("PROJ-99", "In Progress", "2026-09-28T12:00:00Z", "2026-09-30T08:00:00Z"))
	cfg := watchedJiraCfg()

	st, err := runIssueForTest(t, cfg, "PROJ-99", "added")
	if err != nil {
		t.Fatalf("run issue PROJ-99: %v", err)
	}

	ent, found, err := st.GetEntity("acme/widgets", "issue", "PROJ-99")
	if err != nil || !found {
		t.Fatalf("GetEntity: found=%v err=%v", found, err)
	}
	facts, err := interpret.DeriveIssueAttentionFacts(ent.Facts, cfg)
	if err != nil {
		t.Fatalf("DeriveIssueAttentionFacts: %v", err)
	}
	wantSince := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	wantOperator := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	if facts.StatusCategory != interpret.IssueStatusCategoryInProgress || !facts.InProgressSince.Equal(wantSince) ||
		!facts.OperatorFactsKnown || !facts.OperatorUpdatedAt.Equal(wantOperator) || facts.Assignee != "Someone" {
		t.Errorf("facts = %+v", facts)
	}

	// The facts reach the evaluator's view through the projector.
	views, err := attention.Project(st, "acme/widgets", cfg)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	var found2 bool
	for _, v := range views {
		if v.Type == "issue" && v.ID == "PROJ-99" {
			found2 = true
			if f, ok := v.IssueFacts(); !ok || f.StatusCategory != interpret.IssueStatusCategoryInProgress {
				t.Errorf("view issue facts = %+v ok=%v", f, ok)
			}
		}
	}
	if !found2 {
		t.Errorf("issue PROJ-99 was not projected: %d views", len(views))
	}
}

// Without watch.issue.queries the command keeps its documented behavior: it
// never gathers (no pg-connector is needed, and none is installed here), and
// stores no issue entity.
func TestRunCmdIssue_Jira_DoesNotHydrateWithoutWatchConfig(t *testing.T) {
	installFakePGConnector(t, `echo "pg-connector must not be called" >&2; exit 1`)
	st, err := runIssueForTest(t, jiraTestCfg(), "PROJ-99", "added")
	if err != nil {
		t.Fatalf("run issue PROJ-99: %v", err)
	}
	if _, found, _ := st.GetEntity("acme/widgets", "issue", "PROJ-99"); found {
		t.Error("an issue entity was stored although no watch.issue.queries is configured")
	}
}

// A failed hydration is reported, but never stops the linked PRs from being
// re-interpreted.
func TestRunCmdIssue_Jira_HydrationFailureStillReinterpretsPRs(t *testing.T) {
	installFakePGConnector(t, `exit 4`) // every key not_found
	cfg := watchedJiraCfg()

	st, reopen := newRunStore(t, cfg)
	seedPRFacts(t, st, "acme/widgets#7")
	seedXref(t, st, "acme/widgets#7", "PROJ-99")

	runErr := runIssue(t, "PROJ-99", "changed")
	if runErr == nil || !strings.Contains(runErr.Error(), "hydrate issue") {
		t.Fatalf("run issue: err = %v, want a hydration error", runErr)
	}
	if _, found, _ := reopen().GetInterpretation("acme/widgets", entityTypePR, "acme/widgets#7"); !found {
		t.Error("the linked PR was not re-interpreted after the hydration failure")
	}
}

func TestRunCmdIssue_Jira_RemovedChange(t *testing.T) {
	cfg := watchedJiraCfg()
	t.Run("unknown issue is a no-op", func(t *testing.T) {
		installFakePGConnector(t, `echo "pg-connector must not be called" >&2; exit 1`)
		st, err := runIssueForTest(t, cfg, "PROJ-99", "removed")
		if err != nil {
			t.Fatalf("run issue PROJ-99 --change removed: %v", err)
		}
		if _, found, _ := st.GetEntity("acme/widgets", "issue", "PROJ-99"); found {
			t.Error("a removed, never-stored issue must not be stored")
		}
	})
	t.Run("a stored issue is re-read so the row shows what took it out of the watched set", func(t *testing.T) {
		installFakePGConnector(t, issueShowScript("PROJ-99", "Done", "2026-10-01T00:00:00Z", ""))
		_, reopen := newRunStore(t, cfg)
		if err := runIssue(t, "PROJ-99", "added"); err != nil {
			t.Fatalf("run issue (added): %v", err)
		}
		installFakePGConnector(t, issueShowScript("PROJ-99", "Closed", "2026-10-02T00:00:00Z", ""))
		if err := runIssue(t, "PROJ-99", "removed"); err != nil {
			t.Fatalf("run issue (removed): %v", err)
		}
		ent, found, err := reopen().GetEntity("acme/widgets", "issue", "PROJ-99")
		if err != nil || !found {
			t.Fatalf("GetEntity: found=%v err=%v", found, err)
		}
		if !strings.Contains(ent.Facts, `"state":"Closed"`) {
			t.Errorf("facts = %s, want the re-read state", ent.Facts)
		}
	})
}

// issueShowAndDepsScript is a fake pg-connector that answers `issue show` and
// `issue deps <key> --full` for key, appending every call's arguments to
// logPath so a test can see which verbs ran.
func issueShowAndDepsScript(key, logPath string) string {
	return fmt.Sprintf(`
echo "$*" >> %[2]q
case "$1 $2 $3" in
  "issue show %[1]s")
    printf '%%s' '{"protocolVersion":1,"schemaVersion":7,"result":{"id":"%[1]s","title":"t","state":"open","assignee":"Someone","issue_type":"Task","as_of":"2026-10-06T00:00:00Z","deps":[{"id":"bd-blocker","type":"blocks"}]}}'
    exit 0;;
  "issue deps %[1]s")
    printf '%%s' '{"protocolVersion":1,"schemaVersion":7,"result":{"ids":["bd-blocker"],"entities":[]}}'
    exit 0;;
esac
exit 4`, key, logPath)
}

// hydration.read_issue_deps switches `issue deps --full` on for the gatherer
// pipeline.New builds, the one run, refresh and changes all hydrate through:
// off (the default) it is never called and the stored facts carry no
// issue_deps; on, it is called once and its result is stored beside issue_show.
func TestRunCmdIssue_HydrationReadIssueDepsSwitch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		on       bool
		wantDeps bool
	}{
		{"off by default", false, false},
		{"on", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "calls.log")
			installFakePGConnector(t, issueShowAndDepsScript("PROJ-99", logPath))
			cfg := watchedJiraCfg()
			cfg.Hydration.ReadIssueDeps = tc.on

			st, err := runIssueForTest(t, cfg, "PROJ-99", "added")
			if err != nil {
				t.Fatalf("run issue PROJ-99: %v", err)
			}
			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read call log: %v", err)
			}
			calledDeps := strings.Contains(string(raw), "issue deps PROJ-99 --full")
			if calledDeps != tc.wantDeps {
				t.Errorf("issue deps --full called = %v, want %v; calls:\n%s", calledDeps, tc.wantDeps, raw)
			}
			ent, found, err := st.GetEntity("acme/widgets", "issue", "PROJ-99")
			if err != nil || !found {
				t.Fatalf("GetEntity: found=%v err=%v", found, err)
			}
			if has := strings.Contains(ent.Facts, `"issue_deps"`); has != tc.wantDeps {
				t.Errorf("stored facts carry issue_deps = %v, want %v: %s", has, tc.wantDeps, ent.Facts)
			}
			if !strings.Contains(ent.Facts, `"id":"bd-blocker","type":"blocks"`) {
				t.Errorf("stored facts lost the direct issue_show.deps edge: %s", ent.Facts)
			}
		})
	}
}

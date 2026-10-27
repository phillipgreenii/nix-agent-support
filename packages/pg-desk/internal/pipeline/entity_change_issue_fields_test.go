package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

// owningInstance is the backend instance the owning-backend cases pin.
const owningInstance = "owning-backend"

// ownerCfg loads a config whose watched issue query q-own is owned by
// owningInstance.
func ownerCfg(t *testing.T) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "self_login: me\nrepos:\n  - remote: " + ecRepo + "\n" +
		"watch:\n  issue:\n    queries: [q-own]\n    backends:\n      q-own:\n        instance: " + owningInstance + "\n        owns_changes: true\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	return cfg
}

// fieldIssue renders an issue gather result with the given labels, priority
// and updated_at (the facts also carry as_of, which always moves).
func fieldIssue(t *testing.T, labels []string, priority, updatedAt, asOf string) gather.GatherResult {
	t.Helper()
	show := map[string]any{
		"id": "i-1", "owner": "me", "issue_type": "task", "state": "open", "assignee": "me",
		"labels": labels, "priority": priority, "updated_at": updatedAt, "as_of": asOf,
	}
	showJSON, err := json.Marshal(show)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(gather.IssueFacts{IssueShow: showJSON})
	if err != nil {
		t.Fatal(err)
	}
	return gather.GatherResult{Payload: payload, AsOf: asOf}
}

// owningPipeline is ecPipeline with the owner config; the entity's backend
// column is set to backend ("" leaves it NULL) once the first row exists.
func owningPipeline(t *testing.T, res *gather.GatherResult, backend string) *Pipeline {
	t.Helper()
	var gerr error
	p := ecPipeline(t, res, &gerr)
	p.cfg = ownerCfg(t)
	mustRun(t, p, "issue", "i-1", gather.ChangeAdded, EntityChangeOptions{})
	if backend != "" {
		if err := p.store.SetEntityColumns(ecRepo, "issue", "i-1", &backend, nil); err != nil {
			t.Fatalf("SetEntityColumns: %v", err)
		}
	}
	return p
}

// T-3 (pipeline level): a labels-only and a priority-only edit each write
// exactly one fields_changed row and one emission, for beads (NULL backend)
// and for an owning backend alike.
func TestRunEntityChange_IssueLabelsOnlyAndPriorityOnlyEmitFieldsChanged(t *testing.T) {
	for _, backend := range []string{"", owningInstance} {
		for _, tc := range []struct {
			name           string
			labels         []string
			priority       string
			wantRowsBefore int
		}{
			{"labels only", []string{"a", "b"}, "P2", 1},
			{"priority only", []string{"a"}, "P1", 1},
		} {
			t.Run(tc.name+" backend="+backend, func(t *testing.T) {
				res := fieldIssue(t, []string{"a"}, "P2", "2026-10-01T10:00:00Z", "2026-10-01T10:00:00Z")
				p := owningPipeline(t, &res, backend)

				res = fieldIssue(t, tc.labels, tc.priority, "2026-10-01T11:00:00Z", "2026-10-01T11:00:00Z")
				r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
				if !r.Written || !reflect.DeepEqual(r.Kinds, []string{"fields_changed"}) {
					t.Fatalf("result = %+v, want one emission of fields_changed", r)
				}
				rows := p.changeRows(t, "issue", "i-1")
				if len(rows) != tc.wantRowsBefore+1 || !reflect.DeepEqual(rows[len(rows)-1].Kinds, []string{"fields_changed"}) {
					t.Fatalf("rows = %+v, want one new fields_changed row", rows)
				}
			})
		}
	}
}

// INV-ICD-6: an updated_at-only advance writes no row for an owning backend
// and the comments_changed proxy row for beads.
func TestRunEntityChange_IssueUpdatedAtOnlyProxyOwningVersusBeads(t *testing.T) {
	cases := []struct {
		name      string
		backend   string
		wantKinds []string
	}{
		{"beads keeps the proxy", "", []string{"comments_changed"}},
		{"owning backend retires it", owningInstance, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := fieldIssue(t, []string{"a"}, "P2", "2026-10-01T10:00:00Z", "2026-10-01T10:00:00Z")
			p := owningPipeline(t, &res, tc.backend)

			res = fieldIssue(t, []string{"a"}, "P2", "2026-10-01T12:00:00Z", "2026-10-01T12:00:00Z")
			r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
			if !reflect.DeepEqual(r.Kinds, tc.wantKinds) {
				t.Fatalf("kinds = %v, want %v", r.Kinds, tc.wantKinds)
			}
			wantRows := 1 + len(tc.wantKinds)
			if rows := p.changeRows(t, "issue", "i-1"); len(rows) != wantRows {
				t.Fatalf("rows = %+v, want %d", rows, wantRows)
			}
		})
	}
}

// A backend column naming a NON-owning instance keeps the proxy.
func TestRunEntityChange_IssueNonOwningInstanceKeepsProxy(t *testing.T) {
	res := fieldIssue(t, []string{"a"}, "P2", "2026-10-01T10:00:00Z", "2026-10-01T10:00:00Z")
	p := owningPipeline(t, &res, "beads-instance")
	res = fieldIssue(t, []string{"a"}, "P2", "2026-10-01T12:00:00Z", "2026-10-01T12:00:00Z")
	if r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{}); !reflect.DeepEqual(r.Kinds, []string{"comments_changed"}) {
		t.Fatalf("kinds = %v, want [comments_changed]", r.Kinds)
	}
}

// T-12 (beads): a decider write (a label) comes back as one content row, and
// a second hydration of the unchanged result writes nothing.
func TestRunEntityChange_IssueDeciderLabelWriteIsOneRowThenNone(t *testing.T) {
	res := fieldIssue(t, []string{"a"}, "P2", "2026-10-01T10:00:00Z", "2026-10-01T10:00:00Z")
	p := owningPipeline(t, &res, "")

	res = fieldIssue(t, []string{"a", "claimed"}, "P2", "2026-10-01T10:05:00Z", "2026-10-01T10:05:00Z")
	r := mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if !reflect.DeepEqual(r.Kinds, []string{"fields_changed"}) {
		t.Fatalf("decider write kinds = %v, want [fields_changed]", r.Kinds)
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}

	// The same result again (even with a later as_of): no new row.
	res = fieldIssue(t, []string{"a", "claimed"}, "P2", "2026-10-01T10:05:00Z", "2026-10-01T10:30:00Z")
	r = mustRun(t, p, "issue", "i-1", gather.ChangeChanged, EntityChangeOptions{})
	if len(r.Kinds) != 0 {
		t.Fatalf("unchanged re-hydration kinds = %v, want none", r.Kinds)
	}
	if rows := p.changeRows(t, "issue", "i-1"); len(rows) != 2 {
		t.Fatalf("rows = %+v, want still 2", rows)
	}
}

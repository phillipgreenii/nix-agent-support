package view

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(name string) string { return filepath.Join("..", "..", "testdata", name) }

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixture(name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseMinimalPRView(t *testing.T) {
	data := mustRead(t, "pr_view_minimal.json")
	v, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if v.Contract != "pg-desk.view/v1" || v.Type != "pr" || v.ID != "acme/widgets#42" ||
		v.Version != 7 || v.AsOf != "2026-10-01T12:00:00Z" || v.Stale {
		t.Fatalf("header: %+v", v)
	}
	if string(v.Raw) != string(data) {
		t.Fatalf("Raw is not the printed JSON")
	}
	s := v.Snapshot
	if s.Repo != "acme/widgets" || s.Number != 42 || s.Title != "Add retry to client" || s.State != "open" ||
		s.Branch != "feature/retry" || s.Base != "main" || s.Author != "teammate" || s.Draft || s.Merged ||
		!reflect.DeepEqual(s.Labels, []string{"bug", "needs-review"}) ||
		s.HeadSHA != "9f3c1e2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || s.BaseSHA == "" ||
		s.Mergeable != "MERGEABLE" || s.ChecksRollup != "success" ||
		s.ReviewDecision != "REVIEW_REQUIRED" || s.NodeID != "PR_node_42" {
		t.Fatalf("snapshot: %+v", s)
	}
	if len(s.Comments) != 2 || s.Comments[0] != (Comment{ID: "c1", Author: "review-bot", Body: "please add a test"}) ||
		!s.Comments[1].Resolved {
		t.Fatalf("comments: %+v", s.Comments)
	}
	if !strings.Contains(string(v.SnapshotRaw), `"node_id": "PR_node_42"`) {
		t.Fatalf("SnapshotRaw not preserved: %s", v.SnapshotRaw)
	}
	d := v.Decorations
	if d.Relationship != "mine" || d.Urgency != "ready" || d.Category != "ready-to-land" || len(d.Dispositions) != 2 {
		t.Fatalf("decorations: %+v", d)
	}
	if d.Dispositions[0].Override != nil || d.Dispositions[1].Override == nil || *d.Dispositions[1].Override != "ignore" ||
		d.Dispositions[1].CommentID != "c2" || d.Dispositions[1].Computed != "resolved" {
		t.Fatalf("dispositions: %+v", d.Dispositions)
	}
	a := v.Annotations
	if a.Hidden.Value || a.Hidden.Reason != nil || a.WIP || a.ForceReview || a.ReadyToLand != nil ||
		!reflect.DeepEqual(a.Suppress, []string{"fix-ci"}) ||
		a.Decider["pr"]["last_seq"] != "5" {
		t.Fatalf("annotations: %+v", a)
	}
	if len(v.Links) != 2 {
		t.Fatalf("links: %+v", v.Links)
	}
	l := v.Links[0]
	if l.Type != "issue" || l.ID != "bd-1" || l.Relation != "work" || l.URL == "" || l.State != "open" ||
		!reflect.DeepEqual(l.Labels, []string{"pr-fix"}) || l.Metadata["kind"] != "fix-ci" || l.Assignee != "" ||
		!reflect.DeepEqual(l.Origins, []string{"derived:branch-name", "external:agent"}) || l.Title != "" {
		t.Fatalf("link0: %+v", l)
	}
	if v.Links[1].Type != "thread" || len(v.Links[1].Origins) != 0 {
		t.Fatalf("link1: %+v", v.Links[1])
	}
}

func TestParseNotYetLandedMembers(t *testing.T) {
	v, err := Parse(mustRead(t, "pr_view_new_members.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Stale || !v.Annotations.Hidden.Value || v.Annotations.Hidden.Reason == nil ||
		*v.Annotations.Hidden.Reason != "duplicate" || !v.Annotations.WIP || !v.Annotations.ForceReview {
		t.Fatalf("annotations: %+v", v.Annotations)
	}
	if v.Annotations.ReadyToLand == nil || !*v.Annotations.ReadyToLand {
		t.Fatalf("ready_to_land not decoded: %+v", v.Annotations.ReadyToLand)
	}
	l := v.Links[0]
	if l.Title != "Fix the docs build" {
		t.Fatalf("link title: %+v", l)
	}
	// Scalar metadata values are rendered as their JSON text.
	if l.Metadata["seq"] != "3" || l.Metadata["flag"] != "true" || l.Metadata["kind"] != "docs" {
		t.Fatalf("link metadata: %+v", l.Metadata)
	}
	// Members not decoded into typed shapes stay reachable through Raw.
	if !strings.Contains(string(v.Raw), `"failed_checks"`) || !strings.Contains(string(v.Raw), `"force_review_sha"`) {
		t.Fatalf("Raw lost later members")
	}
}

func TestParseToleratesUnknownMembers(t *testing.T) {
	v, err := Parse(mustRead(t, "pr_view_unknown_member.json"))
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "acme/widgets#44" || v.Snapshot.Number != 44 || v.Decorations.Relationship != "team" {
		t.Fatalf("view: %+v", v)
	}
}

func TestParseRejectsWrongContract(t *testing.T) {
	_, err := Parse(mustRead(t, "pr_view_wrong_contract.json"))
	if err == nil || !strings.Contains(err.Error(), "pg-desk.view/v2") {
		t.Fatalf("want contract error naming the bad value, got %v", err)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("expected error")
	}
	if _, err := Parse(nil); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestParseIssueViewKeepsRawSnapshotAndZeroPRSnapshot(t *testing.T) {
	v, err := Parse([]byte(`{"contract":"pg-desk.view/v1","type":"issue","id":"bd-1","version":1,"as_of":"x","stale":false,
		"snapshot":{"id":"bd-1","title":"an issue","number":"not-a-pr-number"},
		"decorations":{"relationship":"","dispositions":[],"urgency":"","category":""},
		"annotations":{"hidden":{"value":false,"reason":null},"wip":false,"suppress":[],"force_review":false,"decider":{}},
		"links":[],"links_as_of":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Snapshot, PRSnapshot{}) {
		t.Fatalf("issue view must leave PRSnapshot zero: %+v", v.Snapshot)
	}
	if !strings.Contains(string(v.SnapshotRaw), "an issue") {
		t.Fatalf("SnapshotRaw: %s", v.SnapshotRaw)
	}
}

func TestParseNullSnapshot(t *testing.T) {
	v, err := Parse([]byte(`{"contract":"pg-desk.view/v1","type":"pr","id":"x","snapshot":null,"links":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Snapshot, PRSnapshot{}) {
		t.Fatalf("snapshot: %+v", v.Snapshot)
	}
}

func TestReadExecsPgDeskShowJSON(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	withHelper(t, "GO_HELPER_STDOUT_FILE="+fixture("pr_view_minimal.json"), "GO_HELPER_ARGS_FILE="+argsFile)
	v, err := Read(context.Background(), "pr", "acme/widgets#42")
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "acme/widgets#42" {
		t.Fatalf("view: %+v", v)
	}
	got, _ := os.ReadFile(argsFile)
	if string(got) != "pr\nshow\nacme/widgets#42\n--json" {
		t.Fatalf("pg-desk argv = %q", got)
	}
}

func TestReadFailureModes(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"non-zero exit", []string{"GO_HELPER_EXIT=1", "GO_HELPER_STDERR=boom: no such entity"}, "boom: no such entity"},
		{"unparseable stdout", []string{"GO_HELPER_STDOUT=<html>"}, "parse"},
		{"wrong contract", []string{"GO_HELPER_STDOUT_FILE=" + fixture("pr_view_wrong_contract.json")}, "pg-desk.view/v2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withHelper(t, c.env...)
			v, err := Read(context.Background(), "pr", "x")
			if err == nil || v != nil {
				t.Fatalf("want error, got v=%v err=%v", v, err)
			}
			if !strings.Contains(err.Error(), "pg-desk") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q should name pg-desk and contain %q", err, c.want)
			}
		})
	}
}

func TestReadWhenPgDeskCannotBeStarted(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Read(context.Background(), "pr", "x")
	if err == nil || !strings.Contains(err.Error(), "pg-desk") {
		t.Fatalf("want error naming pg-desk, got %v", err)
	}
}

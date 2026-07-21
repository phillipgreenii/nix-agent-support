package proto

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
)

// TestDirectoryPRInfoRoundTrip proves a Directory's PRInfo — including the
// State field (F3) — survives FromTree -> Marshal -> Unmarshal -> ToTree. State
// is the field translate.go historically dropped (the proto already carried
// it); Number/Title/URL ride along to pin the whole struct. This is the
// domain<->proto plumbing guard the design (F6) requires.
func TestDirectoryPRInfoRoundTrip(t *testing.T) {
	in := &aggregate.Tree{
		Dirs: []*aggregate.Directory{
			{
				Path:   "/repo",
				Branch: "feat/pr",
				PRInfo: &session.PRInfo{
					Number: 42,
					Title:  "Add the thing",
					State:  "OPEN",
					URL:    "https://github.com/owner/repo/pull/42",
				},
			},
		},
	}
	ds := FromTree(in)

	b, err := proto.Marshal(ds)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out DaemonState
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := ToTree(&out)

	if len(got.Dirs) != 1 {
		t.Fatalf("want 1 dir, got %d", len(got.Dirs))
	}
	pr := got.Dirs[0].PRInfo
	if pr == nil {
		t.Fatal("PRInfo = nil, want present after round-trip")
	}
	if pr.Number != 42 {
		t.Errorf("Number = %d, want 42", pr.Number)
	}
	if pr.State != "OPEN" {
		t.Errorf("State = %q, want OPEN (the field F3 threads through)", pr.State)
	}
	if pr.URL != "https://github.com/owner/repo/pull/42" {
		t.Errorf("URL = %q unexpected", pr.URL)
	}
	if pr.Title != "Add the thing" {
		t.Errorf("Title = %q unexpected", pr.Title)
	}
}

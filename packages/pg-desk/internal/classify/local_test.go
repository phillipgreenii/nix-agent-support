package classify

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

const localRepo = "example/repo"

func localLink(fromType, fromID, toType, toID, relation, origin string) store.XrefLink {
	return store.XrefLink{
		Repo: localRepo, FromType: fromType, FromID: fromID, ToType: toType, ToID: toID,
		Relation: relation, Origin: origin,
	}
}

func localNever(prID, issueID string) bool { return false }

func localAlways(prID, issueID string) bool { return true }

func TestLocalDiffLinks(t *testing.T) {
	derivedWork := localLink("issue", "I-1", "pr", "r#1", "work", "derived:work-item")
	derivedParent := localLink("issue", "I-2", "issue", "I-1", "parent", "derived:work-item")
	externalWork := localLink("issue", "I-1", "pr", "r#1", "work", "external:alice")

	touched := derivedWork
	touched.LastConfirmed = "2026-10-02T00:00:00Z"
	touched.FirstSeen = "2026-09-01T00:00:00Z"
	touched.Evidence = "other"
	touched.ActedAt = "2026-10-02T00:00:00Z"
	touched.Reason = "because"

	tests := []struct {
		name          string
		before, after []store.XrefLink
		want          []LinkChange
	}{
		{"both empty", nil, nil, nil},
		{"unchanged", []store.XrefLink{derivedWork}, []store.XrefLink{derivedWork}, nil},
		{"only metadata movements are not a change", []store.XrefLink{derivedWork}, []store.XrefLink{touched}, nil},
		{"added", nil, []store.XrefLink{derivedWork}, []LinkChange{{Link: derivedWork, Added: true}}},
		{"removed", []store.XrefLink{derivedWork}, nil, []LinkChange{{Link: derivedWork, Added: false}}},
		{
			"external claim on an already-derived link is an added link",
			[]store.XrefLink{derivedWork},
			[]store.XrefLink{derivedWork, externalWork},
			[]LinkChange{{Link: externalWork, Added: true}},
		},
		{
			"external claim removed while derived stays",
			[]store.XrefLink{derivedWork, externalWork},
			[]store.XrefLink{derivedWork},
			[]LinkChange{{Link: externalWork, Added: false}},
		},
		{
			"relation is part of the identity",
			[]store.XrefLink{localLink("issue", "I-1", "pr", "r#1", "work", "derived:x")},
			[]store.XrefLink{localLink("issue", "I-1", "pr", "r#1", "jira", "derived:x")},
			[]LinkChange{
				{Link: localLink("issue", "I-1", "pr", "r#1", "jira", "derived:x"), Added: true},
				{Link: localLink("issue", "I-1", "pr", "r#1", "work", "derived:x"), Added: false},
			},
		},
		{
			"repo is part of the identity",
			[]store.XrefLink{derivedWork},
			[]store.XrefLink{func() store.XrefLink { l := derivedWork; l.Repo = "example/other"; return l }()},
			[]LinkChange{
				{Link: func() store.XrefLink { l := derivedWork; l.Repo = "example/other"; return l }(), Added: true},
				{Link: derivedWork, Added: false},
			},
		},
		{
			"order is deterministic regardless of input order",
			[]store.XrefLink{derivedWork},
			[]store.XrefLink{derivedParent, externalWork},
			[]LinkChange{
				{Link: derivedWork, Added: false},
				{Link: externalWork, Added: true},
				{Link: derivedParent, Added: true},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DiffLinks(tc.before, tc.after)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DiffLinks = %+v, want %+v", got, tc.want)
			}
		})
	}

	// Reversing the input slices must give the same result.
	a := DiffLinks(nil, []store.XrefLink{derivedParent, externalWork, derivedWork})
	b := DiffLinks(nil, []store.XrefLink{derivedWork, externalWork, derivedParent})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("DiffLinks not order independent: %+v vs %+v", a, b)
	}
}

func TestLocalLinkRecords(t *testing.T) {
	work := localLink("issue", "I-1", "pr", "r#1", "work", "derived:work-item")
	externalWork := localLink("issue", "I-1", "pr", "r#1", "work", "external:alice")
	jira := localLink("pr", "r#1", "issue", "J-9", "jira", "derived:jira-key")
	mention := localLink("thread", "C1/100.1", "pr", "r#1", "mentions", "derived:thread-refs")
	parent := localLink("issue", "I-2", "issue", "I-1", "parent", "derived:work-item")
	extRelated := localLink("issue", "I-5", "issue", "I-6", "related", "external:bob")
	threadThread := localLink("thread", "C1/1.1", "thread", "C1/2.2", "related", "external:bob")

	tests := []struct {
		name    string
		changes []LinkChange
		own     WorkLookup
		want    []Targeted
	}{
		{"no changes", nil, localNever, nil},
		{
			"added derived work link: both ends, pr end is work_changed",
			[]LinkChange{{Link: work, Added: true}},
			localNever,
			[]Targeted{
				{EntityType: "issue", EntityID: "I-1", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindWorkChanged}},
			},
		},
		{
			"external work link counts the same",
			[]LinkChange{{Link: externalWork, Added: true}},
			localNever,
			[]Targeted{
				{EntityType: "issue", EntityID: "I-1", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindWorkChanged}},
			},
		},
		{
			"removed work link still routes work_changed with own false",
			[]LinkChange{{Link: work, Added: false}},
			localNever,
			[]Targeted{
				{EntityType: "issue", EntityID: "I-1", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindWorkChanged}},
			},
		},
		{
			"pr to unrelated issue is link_changed for both",
			[]LinkChange{{Link: jira, Added: true}},
			localNever,
			[]Targeted{
				{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "issue", EntityID: "J-9", Record: Record{Kind: KindLinkChanged}},
			},
		},
		{
			"pr to a child of its anchor (own true) is work_changed",
			[]LinkChange{{Link: jira, Added: true}},
			localAlways,
			[]Targeted{
				{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindWorkChanged}},
				{EntityType: "issue", EntityID: "J-9", Record: Record{Kind: KindLinkChanged}},
			},
		},
		{
			"thread mentioning a pr: pr stays link_changed even if own would say yes for issues",
			[]LinkChange{{Link: mention, Added: true}},
			localAlways,
			[]Targeted{
				{EntityType: "thread", EntityID: "C1/100.1", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindLinkChanged}},
			},
		},
		{
			"issue to issue parent link is link_changed on both",
			[]LinkChange{{Link: parent, Added: true}},
			localAlways,
			[]Targeted{
				{EntityType: "issue", EntityID: "I-2", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "issue", EntityID: "I-1", Record: Record{Kind: KindLinkChanged}},
			},
		},
		{
			"external non-pr links produce link_changed for both ends",
			[]LinkChange{{Link: extRelated, Added: true}, {Link: threadThread, Added: false}},
			localNever,
			[]Targeted{
				{EntityType: "issue", EntityID: "I-5", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "issue", EntityID: "I-6", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "thread", EntityID: "C1/1.1", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "thread", EntityID: "C1/2.2", Record: Record{Kind: KindLinkChanged}},
			},
		},
		{
			"derived and external claim of the same link de-duplicate per entity and kind",
			[]LinkChange{{Link: work, Added: true}, {Link: externalWork, Added: true}},
			localNever,
			[]Targeted{
				{EntityType: "issue", EntityID: "I-1", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindWorkChanged}},
			},
		},
		{
			"same pr gets both kinds when it has a work link and an unrelated link",
			[]LinkChange{{Link: work, Added: true}, {Link: mention, Added: true}},
			localNever,
			[]Targeted{
				{EntityType: "issue", EntityID: "I-1", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindWorkChanged}},
				{EntityType: "thread", EntityID: "C1/100.1", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindLinkChanged}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := LinkRecords(tc.changes, tc.own)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("LinkRecords =\n %+v\nwant\n %+v", got, tc.want)
			}
		})
	}

	t.Run("nil own is tolerated", func(t *testing.T) {
		got := LinkRecords([]LinkChange{{Link: jira, Added: true}}, nil)
		if len(got) != 2 || got[0].Kind != KindLinkChanged {
			t.Fatalf("LinkRecords with nil own = %+v", got)
		}
	})
}

func TestLocalPropagationRecords(t *testing.T) {
	work := localLink("issue", "I-1", "pr", "r#1", "work", "derived:work-item")
	jira := localLink("pr", "r#1", "issue", "J-9", "jira", "derived:jira-key")
	mention := localLink("thread", "C1/100.1", "pr", "r#1", "mentions", "derived:thread-refs")
	extWork := localLink("issue", "I-1", "pr", "r#1", "work", "external:alice")

	tests := []struct {
		name        string
		changedType string
		changedID   string
		links       []store.XrefLink
		own         WorkLookup
		want        []Targeted
	}{
		{"no links", "pr", "r#1", nil, localNever, nil},
		{
			"changed pr: issue, unrelated issue and thread each get one record",
			"pr", "r#1",
			[]store.XrefLink{work, jira, mention},
			localNever,
			[]Targeted{
				{EntityType: "issue", EntityID: "I-1", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "issue", EntityID: "J-9", Record: Record{Kind: KindLinkChanged}},
				{EntityType: "thread", EntityID: "C1/100.1", Record: Record{Kind: KindLinkChanged}},
			},
		},
		{
			"changed work item: its pr gets work_changed",
			"issue", "I-1",
			[]store.XrefLink{work},
			localNever,
			[]Targeted{{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindWorkChanged}}},
		},
		{
			"changed child of the anchor: its pr gets work_changed through own",
			"issue", "I-2",
			[]store.XrefLink{localLink("pr", "r#1", "issue", "I-2", "jira", "derived:jira-key")},
			localAlways,
			[]Targeted{{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindWorkChanged}}},
		},
		{
			"changed unrelated issue: its pr gets link_changed",
			"issue", "J-9",
			[]store.XrefLink{jira},
			localNever,
			[]Targeted{{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindLinkChanged}}},
		},
		{
			"changed thread: the mentioned pr gets link_changed even when own says yes",
			"thread", "C1/100.1",
			[]store.XrefLink{mention},
			localAlways,
			[]Targeted{{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindLinkChanged}}},
		},
		{
			"derived and external claims of one link de-duplicate",
			"issue", "I-1",
			[]store.XrefLink{work, extWork},
			localNever,
			[]Targeted{{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindWorkChanged}}},
		},
		{
			"the changed entity itself is never a target",
			"issue", "I-1",
			[]store.XrefLink{localLink("issue", "I-1", "issue", "I-1", "related", "external:x")},
			localNever,
			nil,
		},
		{
			"links not touching the changed entity are ignored",
			"issue", "I-7",
			[]store.XrefLink{jira},
			localNever,
			nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PropagationRecords(tc.changedType, tc.changedID, tc.links, tc.own)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PropagationRecords =\n %+v\nwant\n %+v", got, tc.want)
			}
		})
	}
}

// TestLocalPropagationIsOneHop documents and pins the one-hop rule: the
// function reads only the links it is given and never re-invokes itself on
// the records it produces, so a propagated record cannot propagate again.
// Chain pr <- issue <- thread: a change to the pr reaches only the issue.
func TestLocalPropagationIsOneHop(t *testing.T) {
	links := []store.XrefLink{
		localLink("issue", "I-1", "pr", "r#1", "work", "derived:work-item"),
		localLink("thread", "C1/1.1", "issue", "I-1", "related", "external:bob"),
	}
	got := PropagationRecords("pr", "r#1", links, localNever)
	if len(got) != 1 || got[0].EntityType != "issue" || got[0].EntityID != "I-1" {
		t.Fatalf("expected only the directly linked issue, got %+v", got)
	}
}

func TestLocalNoWorkItemFromTitleOrDedupKey(t *testing.T) {
	// A link whose evidence looks like a title or dedup key proves nothing:
	// only relation work from an issue to that pr (or own) does.
	l := localLink("pr", "r#1", "issue", "I-1", "jira", "derived:jira-key")
	l.Evidence = "title: fix r#1 (dedup_key=pr:r#1)"
	got := LinkRecords([]LinkChange{{Link: l, Added: true}}, localNever)
	for _, r := range got {
		if r.Kind == KindWorkChanged {
			t.Fatalf("work_changed from title/dedup_key evidence: %+v", got)
		}
	}
	// work relation in the wrong direction (pr -> issue) is not the work
	// item extractor's shape either.
	rev := localLink("pr", "r#1", "issue", "I-1", "work", "external:alice")
	for _, r := range LinkRecords([]LinkChange{{Link: rev, Added: true}}, localNever) {
		if r.Kind == KindWorkChanged {
			t.Fatalf("reversed work link recognized: %+v", r)
		}
	}
}

func TestLocalNoForbiddenImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"internal/sync", "internal/beadref"} {
			if strings.Contains(string(b), `"`) && strings.Contains(importBlock(string(b)), bad) {
				t.Errorf("%s imports %s", e.Name(), bad)
			}
		}
	}
}

// importBlock returns the text of a Go file's import declarations.
func importBlock(src string) string {
	start := strings.Index(src, "import")
	if start < 0 {
		return ""
	}
	rest := src[start:]
	if strings.HasPrefix(rest, "import (") {
		end := strings.Index(rest, ")")
		if end < 0 {
			return rest
		}
		return rest[:end]
	}
	nl := strings.Index(rest, "\n")
	if nl < 0 {
		return rest
	}
	return rest[:nl]
}

func TestLocalNewStoreWorkLookup(t *testing.T) {
	st := store.OpenNewSchemaForTest(t)
	now := "2026-10-02T00:00:00Z"
	put := func(l store.XrefLink) {
		t.Helper()
		l.FirstSeen, l.LastConfirmed = now, now
		if err := st.ReplaceDerivedXrefs(l.Repo, l.FromType, l.FromID, []store.XrefLink{l}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// anchor I-1 -> pr work; child I-2 -> I-1 parent; I-3 unrelated, linked
	// to a different pr; I-4 is a child of the unrelated I-3.
	put(localLink("issue", "I-1", "pr", "r#1", "work", "derived:work-item"))
	put(localLink("issue", "I-2", "issue", "I-1", "parent", "derived:work-item"))
	put(localLink("issue", "I-3", "pr", "r#9", "work", "derived:work-item"))
	put(localLink("issue", "I-4", "issue", "I-3", "parent", "derived:work-item"))
	// A non-work relation to the pr proves nothing.
	put(localLink("issue", "I-5", "pr", "r#1", "jira", "derived:jira-key"))
	// A parent link to an issue that has a jira (not work) link proves nothing.
	put(localLink("issue", "I-6", "issue", "I-5", "parent", "derived:work-item"))

	own := NewStoreWorkLookup(st, localRepo)
	tests := []struct {
		name          string
		prID, issueID string
		want          bool
	}{
		{"anchor with a work link", "r#1", "I-1", true},
		{"child whose parent is the anchor", "r#1", "I-2", true},
		{"unrelated issue", "r#1", "I-3", false},
		{"child of an anchor of a different pr", "r#1", "I-4", false},
		{"anchor of a different pr for that pr", "r#9", "I-3", true},
		{"non-work relation to the pr", "r#1", "I-5", false},
		{"child of a non-work-linked issue", "r#1", "I-6", false},
		{"unknown issue", "r#1", "I-404", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := own(tc.prID, tc.issueID); got != tc.want {
				t.Fatalf("own(%s,%s) = %v, want %v", tc.prID, tc.issueID, got, tc.want)
			}
		})
	}

	t.Run("other repo sees nothing", func(t *testing.T) {
		if NewStoreWorkLookup(st, "example/other")("r#1", "I-1") {
			t.Fatal("lookup crossed repos")
		}
	})

	t.Run("feeds LinkRecords for a child of the anchor", func(t *testing.T) {
		l := localLink("pr", "r#1", "issue", "I-2", "jira", "derived:jira-key")
		got := LinkRecords([]LinkChange{{Link: l, Added: true}}, own)
		want := []Targeted{
			{EntityType: "pr", EntityID: "r#1", Record: Record{Kind: KindWorkChanged}},
			{EntityType: "issue", EntityID: "I-2", Record: Record{Kind: KindLinkChanged}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v want %+v", got, want)
		}
	})
}

func localThread(lastReply string) []byte {
	return []byte(`{"thread_show":{"last_reply_at":"` + lastReply + `"}}`)
}

func localThreadSnap(asOf, lastReply string) Snapshot {
	return Snapshot{Type: "thread", ID: "C1/1.1", Exists: true, Active: true, AsOf: asOf, Payload: localThread(lastReply)}
}

func TestLocalThreadResolved(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour

	// Reply 9 days before now: past the 7d window at now.
	replyOld := now.Add(-9 * 24 * time.Hour)
	replyRFC := replyOld.Format(time.RFC3339)
	replySlack := "1759060800.000200" // 2025-09-28T12:00:00Z, far past
	recent := now.Add(-2 * 24 * time.Hour).Format(time.RFC3339)

	// old hydration at a time still inside the window of replyOld (3 days after).
	oldAsOfInside := replyOld.Add(3 * 24 * time.Hour).Format(time.RFC3339)
	// old hydration already beyond the window (8 days after).
	oldAsOfBeyond := replyOld.Add(8 * 24 * time.Hour).Format(time.RFC3339)
	newAsOf := now.Format(time.RFC3339)

	tests := []struct {
		name     string
		old, new Snapshot
		window   time.Duration
		want     bool
	}{
		{"transition RFC3339", localThreadSnap(oldAsOfInside, replyRFC), localThreadSnap(newAsOf, replyRFC), week, true},
		{"already resolved at old", localThreadSnap(oldAsOfBeyond, replyRFC), localThreadSnap(newAsOf, replyRFC), week, false},
		{"still active", localThreadSnap(oldAsOfInside, recent), localThreadSnap(newAsOf, recent), week, false},
		{"first observation", Snapshot{Type: "thread", ID: "C1/1.1"}, localThreadSnap(newAsOf, replyRFC), week, false},
		{
			"old inactive",
			func() Snapshot { s := localThreadSnap(oldAsOfInside, replyRFC); s.Active = false; return s }(),
			localThreadSnap(newAsOf, replyRFC), week, false,
		},
		{
			"new inactive",
			localThreadSnap(oldAsOfInside, replyRFC),
			func() Snapshot { s := localThreadSnap(newAsOf, replyRFC); s.Active = false; return s }(), week, false,
		},
		{"empty last_reply_at", localThreadSnap(oldAsOfInside, replyRFC), localThreadSnap(newAsOf, ""), week, false},
		{"unparseable last_reply_at", localThreadSnap(oldAsOfInside, replyRFC), localThreadSnap(newAsOf, "yesterday"), week, false},
		{
			"missing payload",
			localThreadSnap(oldAsOfInside, replyRFC),
			Snapshot{Type: "thread", ID: "C1/1.1", Exists: true, Active: true, AsOf: newAsOf},
			week, false,
		},
		{
			"not a thread",
			localThreadSnap(oldAsOfInside, replyRFC),
			func() Snapshot { s := localThreadSnap(newAsOf, replyRFC); s.Type = "pr"; return s }(), week, false,
		},
		{"slack ts, transition", localThreadSnap("2025-09-29T12:00:00Z", replySlack), localThreadSnap(newAsOf, replySlack), week, true},
		{"zero window means 7 days: resolved", localThreadSnap(oldAsOfInside, replyRFC), localThreadSnap(newAsOf, replyRFC), 0, true},
		{"zero window means 7 days: not yet", localThreadSnap(oldAsOfInside, recent), localThreadSnap(newAsOf, recent), 0, false},
		{"negative window means 7 days", localThreadSnap(oldAsOfInside, replyRFC), localThreadSnap(newAsOf, replyRFC), -time.Hour, true},
		{"explicit shorter window fires", localThreadSnap(newAsOf, recent), localThreadSnap(newAsOf, recent), time.Hour, false},
		{"old as_of unparseable never fabricates a transition", localThreadSnap("", replyRFC), localThreadSnap(newAsOf, replyRFC), week, false},
		{
			"old without last_reply_at but observed: resolves once new is past the window",
			localThreadSnap(oldAsOfInside, ""), localThreadSnap(newAsOf, replyRFC), week, true,
		},
		{
			"exactly at the window is not past it",
			localThreadSnap(oldAsOfInside, now.Add(-week).Format(time.RFC3339)),
			localThreadSnap(newAsOf, now.Add(-week).Format(time.RFC3339)), week, false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ThreadResolved(tc.old, tc.new, now, tc.window); got != tc.want {
				t.Fatalf("ThreadResolved = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLocalParseReplyTime(t *testing.T) {
	tests := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"2026-10-02T12:00:00Z", time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), true},
		{"2026-10-02T12:00:00.5Z", time.Date(2026, 10, 2, 12, 0, 0, 500000000, time.UTC), true},
		{"2026-10-02T14:00:00+02:00", time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), true},
		{"1759060800.000200", time.Unix(1759060800, 200000), true},
		{"1759060800", time.Unix(1759060800, 0), true},
		{"", time.Time{}, false},
		{"0", time.Time{}, false},
		{"-5.0", time.Time{}, false},
		{"abc.def", time.Time{}, false},
		{"12.3x", time.Time{}, false},
		{"yesterday", time.Time{}, false},
	}
	for _, tc := range tests {
		got, ok := localParseReplyTime(tc.in)
		if ok != tc.ok || (ok && !got.Equal(tc.want)) {
			t.Errorf("localParseReplyTime(%q) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

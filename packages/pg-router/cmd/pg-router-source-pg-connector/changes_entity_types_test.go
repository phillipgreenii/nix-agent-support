package main

import (
	"strings"
	"testing"
	"time"
)

// calendarEventRow is one pg-connector `calendar changes` row: the entity is
// a schema.CalendarEvent (packages/pg-connector/pkg/schema/calendar.go), which
// carries no head_sha or version, so its retry-window digest is over
// {change, source, entity_id} alone.
func calendarEventRow(change, id, title string) string {
	return `{"change":"` + change + `","source":"pg-connector-calendar-osx-bridge","entity":{` +
		`"id":"` + id + `","title":"` + title + `",` +
		`"start":"2026-10-23T15:00:00Z","end":"2026-10-23T15:30:00Z","calendar_id":"cal-1",` +
		`"self_status":"accepted","calendar_priority":"high","as_of":"2026-10-23T14:00:00Z","stale":false}}`
}

func calendarBody(rows ...string) string {
	return `{"sources":[{"backend":"pg-connector-calendar-osx-bridge","status":"succeeded","version":1,"truncated":false}],` +
		`"changes":[` + strings.Join(rows, ",") + `]}`
}

// "calendar" is a valid <type> for the changes verb: the adapter passes it
// to pg-connector as the capability positional (argv `calendar changes ...`)
// and prints one rawItem per change with the capability as the item type.
func TestChanges_CalendarType_PrintsRawItemsWithCalendarType(t *testing.T) {
	fake := withFactory(t, "changes_body_env",
		"GO_HELPER_BODY="+calendarBody(
			calendarEventRow("added", "evt-1", "Design review"),
			calendarEventRow("changed", "evt-2/RID=20261023T150000Z", "Standup"),
		))

	stdout, stderr, code := runCLI(t, "changes", "calendar", "upcoming", "--consumer", "pg-router-calendar")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}

	want := "calendar changes --query upcoming --consumer pg-router-calendar --output json"
	if got := strings.Join(onlyCall(t, fake).Args, " "); got != want {
		t.Fatalf("child argv = %q, want %q", got, want)
	}

	items := mustUnmarshalItems(t, stdout)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2 (stdout=%q)", len(items), stdout)
	}
	for i, tc := range []struct{ id, title, change string }{
		{"evt-1", "Design review", "added"},
		{"evt-2/RID=20261023T150000Z", "Standup", "changed"},
	} {
		got := items[i]
		if got.ID != tc.id || got.Type != "calendar" || got.Title != tc.title {
			t.Fatalf("items[%d] = %+v, want id=%q type=calendar title=%q", i, got, tc.id, tc.title)
		}
		if got.Metadata["change"] != tc.change || got.Metadata["entity_id"] != tc.id ||
			got.Metadata["source"] != "pg-connector-calendar-osx-bridge" {
			t.Fatalf("items[%d].metadata = %v", i, got.Metadata)
		}
		if got.At != "" || got.ExpiresAt != "" {
			t.Fatalf("items[%d] has at/expiresAt without --retry-window: %+v", i, got)
		}
	}
}

// With --retry-window a calendar item carries <entity id>@<12 hex>, at and
// expiresAt like every other type. A calendar entity has no head_sha or
// version, so a second `changed` report of one event inside the window shares
// the first report's id (the accepted content-only trade-off in the behavior
// doc), while a different change kind gets its own id.
func TestChanges_CalendarType_RetryWindowIDsCoalesceByChangeKind(t *testing.T) {
	pinEmitTime(t)
	withFactory(t, "changes_body_env",
		"GO_HELPER_BODY="+calendarBody(
			calendarEventRow("changed", "evt-1", "Design review"),
			calendarEventRow("changed", "evt-1", "Design review (moved)"),
			calendarEventRow("added", "evt-1", "Design review"),
		))

	stdout, stderr, code := runCLI(t, "changes", "calendar", "upcoming", "--consumer", "c1", "--retry-window", "30m")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3 (stdout=%q)", len(items), stdout)
	}
	for i, it := range items {
		m := suffixedID.FindStringSubmatch(it.ID)
		if m == nil || m[1] != "evt-1" {
			t.Fatalf("items[%d].id = %q, want evt-1@<12 hex>", i, it.ID)
		}
		if it.Metadata["entity_id"] != "evt-1" || it.Type != "calendar" {
			t.Fatalf("items[%d] = %+v", i, it)
		}
		at := pinnedEmitTime.UTC().Truncate(time.Second)
		if it.At != at.Format(time.RFC3339) || it.ExpiresAt != at.Add(30*time.Minute).Format(time.RFC3339) {
			t.Fatalf("items[%d] at/expiresAt = %q/%q", i, it.At, it.ExpiresAt)
		}
	}
	if items[0].ID != items[1].ID {
		t.Fatalf("two changed reports of one calendar event got ids %q and %q, want the same (no head_sha/version in the digest)", items[0].ID, items[1].ID)
	}
	if items[0].ID == items[2].ID {
		t.Fatalf("changed and added reports share id %q, want distinct (change kind is in the digest)", items[0].ID)
	}
}

// A degraded calendar backend is reported the same way as for any other type.
func TestChanges_CalendarType_DegradedBackendIsNamed(t *testing.T) {
	withFactory(t, "changes_body_env", "GO_HELPER_BODY="+
		`{"sources":[{"backend":"pg-connector-calendar-osx-bridge","status":"degraded","version":1,"truncated":false,"reason":"bridge unreachable"}],`+
		`"changes":[`+calendarEventRow("added", "evt-1", "Design review")+`]}`)

	stdout, stderr, code := runCLI(t, "changes", "calendar", "upcoming", "--consumer", "c1")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%q)", code, stderr)
	}
	items := mustUnmarshalItems(t, stdout)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if ds := metadataStrings(t, items[0], "degraded_sources"); len(ds) != 1 || ds[0] != "pg-connector-calendar-osx-bridge" {
		t.Fatalf("degraded_sources = %v", ds)
	}
	reasons, _ := items[0].Metadata["degraded_reasons"].(map[string]any)
	if reasons["pg-connector-calendar-osx-bridge"] != "bridge unreachable" {
		t.Fatalf("degraded_reasons = %v", items[0].Metadata["degraded_reasons"])
	}
}

// "mail" is NOT yet pollable through any adapter verb: pg-connector's mail
// capability has no `changes` verb and its `list` has no --query flag (see
// the helper behavior "reject_query_flag"), so every verb fails the way the
// two-value exit scheme says (exit 1, nothing on stdout, pg-connector's
// stderr relayed). This test pins that visible failure so a deployment that
// polls mail before a mail delta feed exists fails loudly rather than
// silently printing [].
func TestMailType_EveryVerbFailsVisiblyUntilPgConnectorGrowsAMailDeltaFeed(t *testing.T) {
	for _, args := range [][]string{
		{"changes", "mail", "unread", "--consumer", "c1"},
		{"sweep", "mail", "unread"},
		{"list", "mail", "unread", "--backend", "pg-connector-mail-osx-bridge"},
	} {
		t.Run(args[0], func(t *testing.T) {
			withFactory(t, "reject_query_flag")
			stdout, stderr, code := runCLI(t, args...)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1 (stdout=%q stderr=%q)", code, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty on failure", stdout)
			}
			if !strings.Contains(stderr, "unknown flag: --query") {
				t.Fatalf("stderr = %q, want pg-connector's own diagnostic relayed", stderr)
			}
		})
	}
}

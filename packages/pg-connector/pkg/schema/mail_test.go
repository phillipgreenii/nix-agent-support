package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMailMessage_JSONRoundTrip mirrors TestCalendarEvent_JSONRoundTrip:
// every field round-trips through JSON with its documented wire name.
func TestMailMessage_JSONRoundTrip(t *testing.T) {
	in := MailMessage{
		ID:           "<m1@example.invalid>",
		Subject:      "Quarterly report",
		Sender:       "Alice Example <alice@example.invalid>",
		DateReceived: "2026-10-05T12:00:00Z",
		Read:         true,
		Flagged:      true,
		Mailbox:      "INBOX",
		Attachments: []MailAttachment{
			{ID: "att-1", Filename: "report.pdf", MimeType: "application/pdf", Size: 2048},
		},
		Body:            "see attached",
		MailboxPriority: "high",
		AsOf:            "2026-10-05T12:01:00Z",
		Stale:           true,
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out MailMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.ID != in.ID || out.Subject != in.Subject || out.Sender != in.Sender ||
		out.DateReceived != in.DateReceived || out.Read != in.Read || out.Flagged != in.Flagged ||
		out.Mailbox != in.Mailbox || out.Body != in.Body || out.MailboxPriority != in.MailboxPriority ||
		out.AsOf != in.AsOf || out.Stale != in.Stale {
		t.Fatalf("round-trip mismatch (scalars): got %+v, want %+v", out, in)
	}
	if len(out.Attachments) != 1 || out.Attachments[0] != in.Attachments[0] {
		t.Fatalf("attachments round-trip mismatch: got %+v, want %+v", out.Attachments, in.Attachments)
	}
	for _, key := range []string{
		`"id"`, `"subject"`, `"sender"`, `"date_received"`, `"read"`, `"flagged"`, `"mailbox"`,
		`"attachments"`, `"body"`, `"mailbox_priority"`, `"as_of"`, `"stale"`,
		`"filename"`, `"mime_type"`, `"size"`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("wire name %s missing from %s", key, raw)
		}
	}
}

// TestMailMessage_OptionalFieldsOmittedWhenEmpty: only id/subject/sender/
// date_received/read/flagged/mailbox/as_of/stale always appear; the
// attachments, body and mailbox_priority fields are omitempty (the last one
// is expected to be unset — operator ruling 2026-10-05).
func TestMailMessage_OptionalFieldsOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(MailMessage{
		ID:           "m1@example.invalid",
		Subject:      "hi",
		Sender:       "alice@example.invalid",
		DateReceived: "2026-10-05T12:00:00Z",
		Mailbox:      "INBOX",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"m1@example.invalid","subject":"hi","sender":"alice@example.invalid","date_received":"2026-10-05T12:00:00Z","read":false,"flagged":false,"mailbox":"INBOX","as_of":"","stale":false}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
	for _, key := range []string{"attachments", "body", "mailbox_priority"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("expected %q omitted when empty, got %s", key, raw)
		}
	}
}

func TestMailAttachment_OptionalFieldsOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(MailAttachment{ID: "att-1", Filename: "a.bin"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"att-1","filename":"a.bin"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

// TestMailMessage_AsOfAndStale_AlwaysPresentInJSON: as_of/stale must always
// be present, since Stale=false is itself informative.
func TestMailMessage_AsOfAndStale_AlwaysPresentInJSON(t *testing.T) {
	raw, err := json.Marshal(MailMessage{ID: "<m1@example.invalid>", AsOf: "2026-10-05T00:00:00Z", Stale: false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["as_of"]; !ok {
		t.Fatalf("as_of missing from %s", raw)
	}
	if staleVal, ok := out["stale"]; !ok || staleVal != false {
		t.Fatalf("stale = %v (present=%v), want false and present", staleVal, ok)
	}
}

// TestMailMessage_IDIsString: a compile-time assertion that MailMessage.ID
// is string-typed.
func TestMailMessage_IDIsString(t *testing.T) {
	var _ string = MailMessage{}.ID //nolint:staticcheck // QF1011: explicit type IS the assertion; omitting it would infer from the field and defeat the check.
}

// TestMailMessage_MailboxPriority_RoundTripsEndToEnd: the static
// classification tag MUST be on the wire (not only an internal input of one
// backend), mirroring CalendarEvent.CalendarPriority.
func TestMailMessage_MailboxPriority_RoundTripsEndToEnd(t *testing.T) {
	raw, err := json.Marshal(MailMessage{ID: "<m1@example.invalid>", MailboxPriority: "high"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"mailbox_priority":"high"`) {
		t.Fatalf("mailbox_priority missing from wire shape: %s", raw)
	}
	var out MailMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.MailboxPriority != "high" {
		t.Fatalf("mailbox_priority = %q, want %q", out.MailboxPriority, "high")
	}
}

// TestMailListResult_WireShape locks in the list result's shape: cursor is
// always null, and truncated is backend-determined (it round-trips both
// values, unlike the calendar/thread results' unconditional ones).
func TestMailListResult_WireShape(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		raw, err := json.Marshal(MailListResult{
			Entities:   []MailMessage{{ID: "<m1@example.invalid>"}},
			PresentIDs: []string{"<m1@example.invalid>"},
			Cursor:     nil,
			Truncated:  truncated,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out["truncated"] != truncated {
			t.Fatalf("truncated = %v, want %v", out["truncated"], truncated)
		}
		if out["cursor"] != nil {
			t.Fatalf("cursor = %v, want null (this capability has no incremental-listing backend)", out["cursor"])
		}
	}
}

func TestMailAttachmentFile_WireShape(t *testing.T) {
	raw, err := json.Marshal(MailAttachmentFile{MessageID: "m1@example.invalid", AttachmentID: "att-1", Path: "/tmp/example/report.pdf"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"message_id":"m1@example.invalid","attachment_id":"att-1","path":"/tmp/example/report.pdf"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

// TestCurrentSchemaVersions_RegistersMail: MailSchemaVersion is registered
// under "mail" so a schema-version mismatch is detectable.
func TestCurrentSchemaVersions_RegistersMail(t *testing.T) {
	v, ok := CurrentSchemaVersions["mail"]
	if !ok {
		t.Fatal(`CurrentSchemaVersions has no "mail" entry`)
	}
	if v != MailSchemaVersion || MailSchemaVersion != 1 {
		t.Fatalf(`CurrentSchemaVersions["mail"] = %d, MailSchemaVersion = %d, want both 1`, v, MailSchemaVersion)
	}
}

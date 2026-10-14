package wireclient

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// Tests for the OPTIONAL startedAt a handler that adopted a dispatch reports in its
// reply (DEC-EVENT-5, bead pg2-dm6de), decoded as Reply.StartedAt.

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func dispatchReplyBody(t *testing.T, body string) (Reply, error) {
	t.Helper()
	c := &Client{Runner: &fakeRunner{stdout: []byte(body), exitCode: 0}, Command: fixedCommand("h")}
	return c.Dispatch(context.Background(), roles.Role{Name: "r"}, eventqueue.Event{ID: "e", Type: "t"})
}

func TestDispatch_decodesTheReportedStart(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       time.Time
	}{
		{
			"sync reply, UTC second precision",
			`{"schemaVersion":"1","id":"dsp-x","outcome":"delivered","startedAt":"2026-10-12T23:03:46Z"}`,
			time.Date(2026, 10, 12, 23, 3, 46, 0, time.UTC),
		},
		{
			"fractional seconds",
			`{"schemaVersion":"1","id":"dsp-x","outcome":"delivered","startedAt":"2026-10-12T23:03:46.480Z"}`,
			time.Date(2026, 10, 12, 23, 3, 46, 480_000_000, time.UTC),
		},
		{
			"a numeric offset names the same instant",
			`{"schemaVersion":"1","id":"dsp-x","outcome":"delivered","startedAt":"2026-10-12T19:03:46-04:00"}`,
			time.Date(2026, 10, 12, 23, 3, 46, 0, time.UTC),
		},
		{
			"deferred ack",
			`{"schemaVersion":"1","id":"dsp-x","deferred":true,"startedAt":"2026-10-12T23:03:46Z"}`,
			time.Date(2026, 10, 12, 23, 3, 46, 0, time.UTC),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureWarnings(t)
			reply, err := dispatchReplyBody(t, tc.body)
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if !reply.StartedAt.Equal(tc.want) {
				t.Fatalf("Reply.StartedAt = %v, want %v", reply.StartedAt, tc.want)
			}
			if reply.ID != "dsp-x" || (reply.Outcome == "" && !reply.Deferred) {
				t.Fatalf("the rest of the reply was lost: %+v", reply)
			}
			if buf.Len() != 0 {
				t.Fatalf("a well-formed startedAt logged a warning: %q", buf.String())
			}
		})
	}
}

// A reply without the field, or with a null one, is today's reply: the zero time,
// nothing logged.
func TestDispatch_noStartedAtIsTheZeroTime(t *testing.T) {
	for _, body := range []string{
		`{"schemaVersion":"1","id":"dsp-x","outcome":"delivered"}`,
		`{"schemaVersion":"1","id":"dsp-x","outcome":"delivered","startedAt":null}`,
		`{"schemaVersion":"1","id":"dsp-x","deferred":true}`,
	} {
		buf := captureWarnings(t)
		reply, err := dispatchReplyBody(t, body)
		if err != nil {
			t.Fatalf("Dispatch(%s): %v", body, err)
		}
		if !reply.StartedAt.IsZero() {
			t.Fatalf("Dispatch(%s): Reply.StartedAt = %v, want the zero time", body, reply.StartedAt)
		}
		if buf.Len() != 0 {
			t.Fatalf("Dispatch(%s) logged a warning: %q", body, buf.String())
		}
	}
}

// A startedAt that is not an RFC 3339 instant, or not even a string, is ignored with
// a warning, and the dispatch is NOT failed: the field is advisory, and failing a
// dispatch that worked could re-run it.
func TestDispatch_malformedStartedAtIsIgnoredWithAWarning(t *testing.T) {
	for _, tc := range []struct{ name, field string }{
		{"not a time", `"yesterday"`},
		{"empty string", `""`},
		{"a date without a time", `"2026-10-12"`},
		{"no zone", `"2026-10-12T23:03:46"`},
		{"a month of 13", `"2026-13-12T23:03:46Z"`},
		{"a number", `1760310226`},
		{"an object", `{"at":"2026-10-12T23:03:46Z"}`},
		{"a boolean", `true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureWarnings(t)
			reply, err := dispatchReplyBody(t, `{"schemaVersion":"1","id":"dsp-x","outcome":"delivered","startedAt":`+tc.field+`}`)
			if err != nil {
				t.Fatalf("a malformed startedAt failed the dispatch: %v", err)
			}
			if !reply.StartedAt.IsZero() {
				t.Fatalf("Reply.StartedAt = %v, want the zero time", reply.StartedAt)
			}
			if reply.Outcome != "delivered" || reply.ID != "dsp-x" {
				t.Fatalf("the outcome was lost with the bad field: %+v", reply)
			}
			if log := buf.String(); !strings.Contains(log, "ignoring a startedAt") || !strings.Contains(log, "role=r") {
				t.Fatalf("warning = %q, want one that names the role and says the startedAt was ignored", log)
			}
		})
	}
}

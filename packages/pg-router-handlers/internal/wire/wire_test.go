package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router/schemas"
)

// The failure-reason strings, the decline kinds and the busy exit code are a
// handler-to-alert contract: pin every one byte for byte.
func TestBusyDeclineFor_mapsEverySentinel(t *testing.T) {
	cases := []struct {
		err          error
		reason, kind string
	}{
		{executor.ErrPoolCapacityUnknown, "capacity-unknown", "fault"},
		{executor.ErrPoolAtCapacity, "at-capacity", "busy"},
		{executor.ErrLowDisk, "low-disk", "fault"},
		{executor.ErrBusy, "command-busy", "busy"},
		{executor.ErrSessionInFlight, "session-in-flight", "busy"},
		{executor.ErrPoolUnreadable, "pool-unreadable", "fault"},
		{executor.ErrLaunchFailed, "launch-failed", "fault"},
	}
	for _, tc := range cases {
		d, ok := BusyDeclineFor(fmt.Errorf("wrapped: %w", tc.err))
		if !ok || d.Reason != tc.reason || d.Kind != tc.kind {
			t.Errorf("BusyDeclineFor(%v) = %+v, %v; want reason %q kind %q", tc.err, d, ok, tc.reason, tc.kind)
		}
	}
	for _, err := range []error{nil, errors.New("other")} {
		if d, ok := BusyDeclineFor(err); ok || d != (BusyDecline{}) {
			t.Errorf("BusyDeclineFor(%v) = %+v, %v; want no match", err, d, ok)
		}
	}
	if DeclineKindNeutral != "neutral" {
		t.Errorf("DeclineKindNeutral = %q", DeclineKindNeutral)
	}
}

func TestBusyDeclineFor_launchFailedCarriesTailDetail(t *testing.T) {
	tail := strings.Repeat("argv word ", schemas.DeclineDetailMaxLen/10+5) + "DIAGNOSIS"
	err := fmt.Errorf("w: %w", &executor.LaunchFailedError{Tail: tail, Err: executor.ErrLaunchFailed})
	d, ok := BusyDeclineFor(err)
	if !ok || d.Reason != BusyReasonLaunchFailed {
		t.Fatalf("BusyDeclineFor = %+v, %v", d, ok)
	}
	if len(d.Detail) != schemas.DeclineDetailMaxLen || !strings.HasSuffix(d.Detail, "DIAGNOSIS") {
		t.Errorf("detail must keep the END of the text within the bound; got %d bytes %q", len(d.Detail), d.Detail)
	}
}

func TestBoundedDetail_keepsHeadAtRuneBoundary(t *testing.T) {
	text := strings.Repeat("é", schemas.DeclineDetailMaxLen) // 2 bytes per rune
	got := BoundedDetail(text)
	if len(got) > schemas.DeclineDetailMaxLen || !utf8.ValidString(got) || !strings.HasPrefix(text, got) {
		t.Errorf("BoundedDetail: len %d valid %v", len(got), utf8.ValidString(got))
	}
	tail := BoundedTailDetail(text)
	if len(tail) > schemas.DeclineDetailMaxLen || !utf8.ValidString(tail) || !strings.HasSuffix(text, tail) {
		t.Errorf("BoundedTailDetail: len %d valid %v", len(tail), utf8.ValidString(tail))
	}
}

func TestWriteBusyReply_omitsEmptyDetail(t *testing.T) {
	var b bytes.Buffer
	WriteBusyReply(&b, NeutralDecline("skipped-bead-closed", ""))
	var got map[string]any
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["reason"] != "skipped-bead-closed" || got["kind"] != "neutral" || got["schemaVersion"] != schemas.SchemaVersion {
		t.Errorf("reply = %v", got)
	}
	if _, has := got["detail"]; has {
		t.Errorf("an empty detail must be omitted: %v", got)
	}
}

func TestOutcomeReply_startedAtOnlyWhenSet(t *testing.T) {
	if _, has := OutcomeReply("r", "o", time.Time{})["startedAt"]; has {
		t.Error("zero startedAt must be omitted")
	}
	r := OutcomeReply("r", "o", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if r["id"] != "r" || r["outcome"] != "o" || r["startedAt"] == nil {
		t.Errorf("reply = %v", r)
	}
}

// A moot precheck outcome is completed as skipped (bead pg2-6t91v7): an ordinary
// inline-outcome reply plus the optional skipped reason, valid against the
// handler.dispatch-reply schema, and carrying none of the decline body's fields.
func TestSkippedReply_isAnOutcomeReplyWithTheReason(t *testing.T) {
	reply := SkippedReply("dsp-1", "skipped-bead-closed", "pg2-x")
	if err := schemas.Validate("handler.dispatch-reply", reply); err != nil {
		t.Fatalf("the skipped reply does not validate: %v", err)
	}
	if reply["skipped"] != "skipped-bead-closed" || reply["id"] != "dsp-1" || reply["schemaVersion"] != schemas.SchemaVersion {
		t.Fatalf("reply = %v", reply)
	}
	for _, decline := range []string{"reason", "kind", "detail", "deferred", "startedAt"} {
		if _, has := reply[decline]; has {
			t.Errorf("a skipped completion must not carry %q: %v", decline, reply)
		}
	}
	out, _ := reply["outcome"].(string)
	var fields struct {
		Actions []struct {
			Verb string `json:"verb"`
			Refs []struct{ Type, ID string }
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out), &fields); err != nil || len(fields.Actions) != 1 ||
		fields.Actions[0].Verb != "skipped" || len(fields.Actions[0].Refs) != 1 || fields.Actions[0].Refs[0].ID != "pg2-x" {
		t.Fatalf("outcome = %q (%v), want the one skipped action for pg2-x", out, err)
	}
}

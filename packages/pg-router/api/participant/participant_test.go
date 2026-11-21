package participant_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/api/participant"
	"github.com/phillipgreenii/pg-router/conformance/golden"
	"github.com/phillipgreenii/pg-router/schemas"
	"github.com/phillipgreenii/x/exitcode"
)

// decodeStrict decodes data into v, rejecting an unknown property.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// The both-ways test, type to schema and schema to type: for each exported
// message type that has a schema, its golden fixture decodes strictly into the
// type, what the type encodes validates against the hand-written schema, and a
// second decode and encode changes nothing.
func TestGoldenFixturesRoundTripThroughTheSchemas(t *testing.T) {
	cases := []struct {
		messageType string
		newValue    func() any
	}{
		{"handler.dispatch", func() any { return new(participant.DispatchRequest) }},
		{"handler.dispatch-reply", func() any { return new(participant.DispatchReply) }},
		{"handler.postStartup", func() any { return new(participant.LifecycleRequest) }},
		{"handler.preShutdown", func() any { return new(participant.LifecycleRequest) }},
		{"handler.postStartup-reply", func() any { return new(participant.LifecycleReply) }},
		{"handler.preShutdown-reply", func() any { return new(participant.LifecycleReply) }},
	}
	for _, c := range cases {
		t.Run(c.messageType, func(t *testing.T) {
			m, err := golden.Golden(c.messageType)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(m)
			v := c.newValue()
			if err := decodeStrict(raw, v); err != nil {
				t.Fatalf("golden fixture does not decode strictly: %v", err)
			}
			encoded, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if err := schemas.ValidateBytes(c.messageType, encoded); err != nil {
				t.Fatalf("encoded value fails its schema: %v\n%s", err, encoded)
			}
			again := c.newValue()
			if err := decodeStrict(encoded, again); err != nil {
				t.Fatal(err)
			}
			againEncoded, _ := json.Marshal(again)
			if !bytes.Equal(encoded, againEncoded) {
				t.Fatalf("round trip changed the message:\n got %s\nwant %s", againEncoded, encoded)
			}
		})
	}
}

// Every shape of the dispatch reply (inline outcome with the optional skipped,
// startedAt, session and supervisors; deferred; the decline body) validates
// against the schema.
func TestDispatchReplyShapesValidate(t *testing.T) {
	inline := participant.DispatchReply{SchemaVersion: "1", ID: "dsp-1", Outcome: "o", Skipped: "skipped-bead-closed"}
	inline.SetStartedAt(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	inline.SetSupervision("sess-1", 2)
	decline := participant.DeclineReply{SchemaVersion: "1", Reason: "at-capacity", Kind: participant.DeclineKindBusy, Detail: "d"}
	for name, v := range map[string]any{
		"inline":   inline,
		"deferred": participant.DispatchReply{SchemaVersion: "1", ID: "dsp-1", Deferred: true},
		"decline":  decline,
	} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := schemas.ValidateBytes("handler.dispatch-reply", b); err != nil {
			t.Errorf("%s: %v\n%s", name, err, b)
		}
	}
}

// The dispatch request carries the two correlation ids: the dispatch id is the
// request id and the event id is the event's.
func TestDispatchRequestCarriesBothIDs(t *testing.T) {
	req, err := participant.DecodeDispatchRequest([]byte(`{"schemaVersion":"1","id":"dsp-1","event":{"id":"evt-1","type":"t","payload":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.DispatchID() != "dsp-1" || req.EventID() != "evt-1" {
		t.Fatalf("ids = %q, %q", req.DispatchID(), req.EventID())
	}
	if _, err := participant.DecodeDispatchRequest([]byte(`{"id":"d","event":{"id":"e","type":"t"},"surprise":1}`)); err == nil {
		t.Fatal("an unknown property was accepted")
	}
}

func TestSupervisionOnlyWhenBothValid(t *testing.T) {
	for _, c := range []struct {
		session string
		n       int
		ok      bool
	}{{"s", 1, true}, {"", 2, false}, {"s", 0, false}, {"s", -1, false}} {
		var r participant.DispatchReply
		r.SetSupervision(c.session, c.n)
		gotS, gotN := r.ReportedSupervision()
		if (gotS != "") != c.ok || (gotN != 0) != c.ok {
			t.Errorf("(%q,%d): reported (%q,%d), want present=%v", c.session, c.n, gotS, gotN, c.ok)
		}
	}
	// A wrong-typed or half-present pair reads as absent.
	for _, body := range []string{
		`{"session":"s"}`, `{"supervisors":2}`, `{"session":1,"supervisors":2}`,
		`{"session":"s","supervisors":"x"}`, `{"session":"s","supervisors":0}`, `{"session":"","supervisors":2}`,
	} {
		r, err := participant.DecodeDispatchReply([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if s, n := r.ReportedSupervision(); s != "" || n != 0 {
			t.Errorf("%s: reported (%q,%d), want none", body, s, n)
		}
	}
}

func TestReportedStart(t *testing.T) {
	var r participant.DispatchReply
	if got, err := r.ReportedStart(); err != nil || !got.IsZero() {
		t.Fatalf("absent: %v, %v", got, err)
	}
	r.SetStartedAt(time.Time{})
	if len(r.StartedAt) != 0 {
		t.Fatalf("a zero time must leave the field absent, got %s", r.StartedAt)
	}
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	r.SetStartedAt(want)
	if got, err := r.ReportedStart(); err != nil || !got.Equal(want) {
		t.Fatalf("round trip: %v, %v", got, err)
	}
	for _, body := range []string{`{"startedAt":5}`, `{"startedAt":"yesterday"}`} {
		bad, _ := participant.DecodeDispatchReply([]byte(body))
		if got, err := bad.ReportedStart(); err == nil || !got.IsZero() {
			t.Errorf("%s: got %v, %v; want zero and an error", body, got, err)
		}
	}
}

// The decline body decodes leniently: each field on its own, never failing.
func TestDecodeDeclineIsLenient(t *testing.T) {
	d := participant.DecodeDecline([]byte(`{"reason":"at-capacity","kind":7,"detail":"x","extra":true}`))
	if d.Reason != "at-capacity" || d.Kind != "" || d.Detail != "x" {
		t.Fatalf("got %+v", d)
	}
	for _, body := range []string{"", "not json", "[1]", "null"} {
		if got := participant.DecodeDecline([]byte(body)); got != (participant.DeclineReply{}) {
			t.Errorf("%q: got %+v, want zero", body, got)
		}
	}
}

// The outcome codec: what EncodeOutcome writes, FromOutcome reads back; a plain
// token reads as one verb with the default refs; an empty actions array reads as
// no actions.
func TestOutcomeCodec(t *testing.T) {
	in := participant.Report{Actions: []participant.Action{
		{Verb: participant.Closed, Refs: []participant.Ref{{Type: "bead", ID: "a"}}},
		{Verb: participant.Skipped, Refs: []participant.Ref{{Type: "bead", ID: "b"}}},
	}}
	got := participant.FromOutcome(participant.EncodeOutcome(in), nil)
	if len(got.Actions) != 2 || got.Actions[0].Verb != participant.Closed || got.Actions[1].Refs[0].ID != "b" {
		t.Fatalf("round trip = %+v", got)
	}
	def := []participant.Ref{{Type: "bead", ID: "d"}}
	tok := participant.FromOutcome("delivered", def)
	if len(tok.Actions) != 1 || tok.Actions[0].Verb != "delivered" || tok.Actions[0].Refs[0].ID != "d" {
		t.Fatalf("token = %+v", tok)
	}
	if empty := participant.FromOutcome(`{"actions":[]}`, def); len(empty.Actions) != 0 {
		t.Fatalf("empty = %+v", empty)
	}
	noRefs := participant.FromOutcome(`{"actions":[{"verb":"closed"}]}`, def)
	if noRefs.Actions[0].Refs[0].ID != "d" {
		t.Fatalf("an action without refs must take the defaults: %+v", noRefs)
	}
}

// The packages re-export the shared exit codes and define no new one.
func TestExitCodesAreTheSharedWorkspaceCodes(t *testing.T) {
	for name, pair := range map[string][2]int{
		"OK": {participant.ExitOK, exitcode.OK}, "Error": {participant.ExitError, exitcode.Error},
		"Usage": {participant.ExitUsage, exitcode.Usage}, "Unavailable": {participant.ExitUnavailable, exitcode.Unavailable},
		"Config": {participant.ExitConfig, exitcode.Config}, "Busy": {participant.ExitBusy, exitcode.Busy},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %d, shared code is %d", name, pair[0], pair[1])
		}
	}
	if participant.ExitBusy != 9 || participant.ExitUnavailable != 3 {
		t.Errorf("busy=%d unavailable=%d, want 9 and 3", participant.ExitBusy, participant.ExitUnavailable)
	}
}

func TestLifecycleNames(t *testing.T) {
	for state, want := range map[participant.Lifecycle]string{
		participant.Starting: "starting", participant.Started: "started", participant.Stopping: "stopping",
		participant.Stopped: "stopped", participant.Crashing: "crashing",
	} {
		if got := state.String(); got != want {
			t.Errorf("Lifecycle(%d) = %q, want %q", int(state), got, want)
		}
	}
	if got := participant.Lifecycle(99).String(); !strings.Contains(got, "99") {
		t.Errorf("out-of-range = %q, want the value named", got)
	}
}

package wireclient

import "testing"

// The optional `skipped` reason of an inline-outcome handler.dispatch-reply (bead
// pg2-6t91v7) is relayed verbatim as Reply.Skipped, next to the opaque outcome.
func TestDispatch_decodesTheSkippedReason(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"moot completion", `{"schemaVersion":"1","id":"dsp-x","outcome":"{\"actions\":[]}","skipped":"skipped-bead-closed"}`, "skipped-bead-closed"},
		{"real completion has none", `{"schemaVersion":"1","id":"dsp-x","outcome":"delivered"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply, err := dispatchReplyBody(t, tc.body)
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if reply.Skipped != tc.want {
				t.Fatalf("Reply.Skipped = %q, want %q", reply.Skipped, tc.want)
			}
			if reply.Outcome == "" || reply.ID != "dsp-x" {
				t.Fatalf("the rest of the reply was lost: %+v", reply)
			}
		})
	}
}

// The optional session and supervisors of an inline-outcome reply are relayed only
// when BOTH are present and valid: a non-empty string session and an integer
// supervisors of at least 1. A reply without them, with one of them, with an empty
// session or with a bad supervisors keeps both zero and the dispatch still works.
func TestDispatch_decodesTheSupervisionReport(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		session     string
		supervisors int
	}{
		{"both valid", `,"session":"s-1","supervisors":2`, "s-1", 2},
		{"absent", ``, "", 0},
		{"only session", `,"session":"s-1"`, "", 0},
		{"only supervisors", `,"supervisors":2`, "", 0},
		{"empty session", `,"session":"","supervisors":2`, "", 0},
		{"zero supervisors", `,"session":"s-1","supervisors":0`, "", 0},
		{"fractional supervisors", `,"session":"s-1","supervisors":1.5`, "", 0},
		{"string supervisors", `,"session":"s-1","supervisors":"2"`, "", 0},
		{"non-string session", `,"session":7,"supervisors":2`, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply, err := dispatchReplyBody(t, `{"schemaVersion":"1","id":"dsp-x","outcome":"o"`+tc.extra+`}`)
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if reply.Session != tc.session || reply.Supervisors != tc.supervisors {
				t.Fatalf("(Session, Supervisors) = (%q, %d), want (%q, %d)", reply.Session, reply.Supervisors, tc.session, tc.supervisors)
			}
			if reply.ID != "dsp-x" || reply.Outcome != "o" {
				t.Fatalf("the rest of the reply was lost: %+v", reply)
			}
		})
	}
}

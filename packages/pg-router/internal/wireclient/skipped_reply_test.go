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

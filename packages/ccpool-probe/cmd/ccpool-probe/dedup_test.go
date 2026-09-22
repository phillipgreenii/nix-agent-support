package main

import "testing"

func TestDecideActionCreateWhenNoMatch(t *testing.T) {
	f := finding{Fingerprint: "needs-input:sess-1", State: "needs_input"}
	action, match := decideAction(f, nil)
	if action != actionCreate {
		t.Fatalf("action = %v, want actionCreate", action)
	}
	if match != nil {
		t.Fatalf("expected no match, got %+v", match)
	}
}

func TestDecideActionSkipWhenStateUnchanged(t *testing.T) {
	f := finding{Fingerprint: "needs-input:sess-1", State: "needs_input"}
	existing := []connectorIssue{{
		ID: "zr-1",
		Metadata: map[string]string{
			metaFingerprint: "needs-input:sess-1",
			metaState:       "needs_input",
		},
	}}
	action, match := decideAction(f, existing)
	if action != actionSkip {
		t.Fatalf("action = %v, want actionSkip", action)
	}
	if match == nil || match.ID != "zr-1" {
		t.Fatalf("expected match on zr-1, got %+v", match)
	}
}

func TestDecideActionUpdateWhenStateDiffers(t *testing.T) {
	f := finding{Fingerprint: "needs-input:sess-1", State: "needs_input_different"}
	existing := []connectorIssue{{
		ID: "zr-1",
		Metadata: map[string]string{
			metaFingerprint: "needs-input:sess-1",
			metaState:       "needs_input",
		},
	}}
	action, match := decideAction(f, existing)
	if action != actionUpdate {
		t.Fatalf("action = %v, want actionUpdate", action)
	}
	if match == nil || match.ID != "zr-1" {
		t.Fatalf("expected match on zr-1, got %+v", match)
	}
}

func TestMatchExistingExactFingerprintOnly(t *testing.T) {
	existing := []connectorIssue{
		{ID: "zr-1", Metadata: map[string]string{metaFingerprint: "needs-input:sess-1"}},
		{ID: "zr-2", Metadata: map[string]string{metaFingerprint: "needs-input:sess-2"}},
	}
	f := finding{Fingerprint: "needs-input:sess-2"}
	match := matchExisting(f, existing)
	if match == nil || match.ID != "zr-2" {
		t.Fatalf("expected exact match on zr-2, got %+v", match)
	}

	// No fuzzy matching -- a near-miss fingerprint matches nothing.
	noMatch := matchExisting(finding{Fingerprint: "needs-input:sess-3"}, existing)
	if noMatch != nil {
		t.Fatalf("expected no match, got %+v", noMatch)
	}
}

func TestTrackedMetadataCarriesFingerprintAndState(t *testing.T) {
	f := finding{Fingerprint: "zombie-count:+50%", State: "+50%"}
	got := trackedMetadata(f)
	if got[metaFingerprint] != f.Fingerprint {
		t.Errorf("fingerprint metadata = %q", got[metaFingerprint])
	}
	if got[metaState] != f.State {
		t.Errorf("state metadata = %q", got[metaState])
	}
}

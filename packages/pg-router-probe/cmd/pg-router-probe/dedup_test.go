package main

import "testing"

func TestDecideActionNoMatchCreates(t *testing.T) {
	f := finding{Kind: kindGrafanaAlert, Fingerprint: "fp1", State: "active"}
	action, match := decideAction(f, nil)
	if action != actionCreate || match != nil {
		t.Fatalf("got action=%v match=%v", action, match)
	}
}

func TestDecideActionMatchByAlias(t *testing.T) {
	f := finding{Kind: kindGrafanaAlert, Fingerprint: "new-fp", State: "active"}
	existing := []connectorIssue{{
		ID: "zr-1",
		Metadata: map[string]string{
			metaFingerprint:      "old-fp",
			metaFingerprintAlias: "new-fp;another-fp",
			metaState:            "active",
			metaEpisodeCount:     "0",
		},
	}}
	action, match := decideAction(f, existing)
	if action != actionSkip {
		t.Fatalf("expected actionSkip (nothing changed), got %v", action)
	}
	if match == nil || match.ID != "zr-1" {
		t.Fatalf("expected alias match against zr-1, got %v", match)
	}
}

func TestDecideActionGrafanaSkipsWhenNothingChanged(t *testing.T) {
	f := finding{Kind: kindGrafanaAlert, Fingerprint: "fp1", State: "active", EpisodeCount: 2}
	existing := []connectorIssue{{ID: "zr-1", Metadata: map[string]string{
		metaFingerprint:  "fp1",
		metaState:        "active",
		metaEpisodeCount: "2",
	}}}
	action, _ := decideAction(f, existing)
	if action != actionSkip {
		t.Fatalf("got %v", action)
	}
}

func TestDecideActionGrafanaUpdatesOnStateChange(t *testing.T) {
	f := finding{Kind: kindGrafanaAlert, Fingerprint: "fp1", State: "resolved-then-refiring", EpisodeCount: 2}
	existing := []connectorIssue{{ID: "zr-1", Metadata: map[string]string{
		metaFingerprint:  "fp1",
		metaState:        "active",
		metaEpisodeCount: "2",
	}}}
	action, match := decideAction(f, existing)
	if action != actionUpdate || match == nil {
		t.Fatalf("got action=%v match=%v", action, match)
	}
}

func TestDecideActionGrafanaUpdatesOnEpisodeCountChange(t *testing.T) {
	f := finding{Kind: kindGrafanaAlert, Fingerprint: "fp1", State: "active", EpisodeCount: 5}
	existing := []connectorIssue{{ID: "zr-1", Metadata: map[string]string{
		metaFingerprint:  "fp1",
		metaState:        "active",
		metaEpisodeCount: "2",
	}}}
	action, _ := decideAction(f, existing)
	if action != actionUpdate {
		t.Fatalf("got %v", action)
	}
}

func TestDecideActionQueueGrowthSkipsSameBand(t *testing.T) {
	f := finding{Kind: kindQueueGrowth, Fingerprint: "queue-growth:backlog", State: "medium"}
	existing := []connectorIssue{{ID: "zr-1", Metadata: map[string]string{
		metaFingerprint: "queue-growth:backlog",
		metaState:       "medium",
	}}}
	action, _ := decideAction(f, existing)
	if action != actionSkip {
		t.Fatalf("got %v", action)
	}
}

func TestDecideActionQueueGrowthUpdatesOnNewBand(t *testing.T) {
	f := finding{Kind: kindQueueGrowth, Fingerprint: "queue-growth:backlog", State: "high"}
	existing := []connectorIssue{{ID: "zr-1", Metadata: map[string]string{
		metaFingerprint: "queue-growth:backlog",
		metaState:       "medium",
	}}}
	action, _ := decideAction(f, existing)
	if action != actionUpdate {
		t.Fatalf("got %v", action)
	}
}

func TestDecideActionBinaryHashSkipsSameHash(t *testing.T) {
	f := finding{Kind: kindBinaryHashMismatch, Fingerprint: binaryHashMismatchFingerprint, State: "def456"}
	existing := []connectorIssue{{ID: "zr-1", Metadata: map[string]string{
		metaFingerprint: binaryHashMismatchFingerprint,
		metaState:       "def456",
	}}}
	action, _ := decideAction(f, existing)
	if action != actionSkip {
		t.Fatalf("got %v", action)
	}
}

func TestDecideActionBinaryHashUpdatesOnNewHash(t *testing.T) {
	f := finding{Kind: kindBinaryHashMismatch, Fingerprint: binaryHashMismatchFingerprint, State: "ghi789"}
	existing := []connectorIssue{{ID: "zr-1", Metadata: map[string]string{
		metaFingerprint: binaryHashMismatchFingerprint,
		metaState:       "def456",
	}}}
	action, _ := decideAction(f, existing)
	if action != actionUpdate {
		t.Fatalf("got %v", action)
	}
}

func TestTrackedMetadataRoundTripsThroughDecideAction(t *testing.T) {
	f := finding{Kind: kindGrafanaAlert, Fingerprint: "fp1", Aliases: []string{"fp1", "fp0"}, State: "active", EpisodeCount: 4}
	tracked := trackedMetadata(f)
	existing := []connectorIssue{{ID: "zr-1", Metadata: tracked}}
	// The exact same finding, matched against the metadata it would have
	// just written, must be a no-op ("nothing new").
	action, _ := decideAction(f, existing)
	if action != actionSkip {
		t.Fatalf("expected a self-consistent round trip to skip, got %v (tracked=%v)", action, tracked)
	}
}

func TestSplitAndJoinAliasesRoundTrip(t *testing.T) {
	aliases := []string{"a", "b", "c"}
	joined := joinAliases(aliases)
	if joined != "a;b;c" {
		t.Fatalf("got %q", joined)
	}
	got := splitAliases(joined)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
}

func TestSplitAliasesEmpty(t *testing.T) {
	if got := splitAliases(""); got != nil {
		t.Fatalf("got %v", got)
	}
}

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Subject: one open bead per alert fingerprint. A fingerprint that fires
// again while its bead is open adds a comment to that bead and files no new
// one, whatever labels the open bead carries and whether the second firing
// is in a later tick or in the same run as the create.

func fpAlert(startsAt time.Time) grafanaAlert {
	a := originAlert("")
	a.StartsAt = startsAt
	return a
}

// A later tick: the open bead (handed to the operator, so it carries human
// and attention) is listed by the dedup query; a new episode of the same
// fingerprint comments on it and creates nothing.
func TestRefiringFingerprintCommentsOnOpenBeadAndFilesNoNewBead(t *testing.T) {
	for name, labels := range map[string][]string{
		"escalated":       {"escalated"},
		"escalated+human": {"escalated", "human", "attention"},
		"handed-off":      {"human", "attention"},
	} {
		t.Run(name, func(t *testing.T) {
			opts := episodeOpts(t)
			open := connectorIssue{ID: "zr-open", Labels: labels, Metadata: map[string]string{
				metaFingerprint: originFP, metaState: "active", metaEpisodeCount: "1",
			}}
			seedAlertState(t, opts.snapshotPath, originFP, alertState{LastStartsAt: started, Episodes: []time.Time{started}, LastNoted: t0})
			spy := &spyDeps{
				fetchAlertsFn: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
					return []grafanaAlert{fpAlert(started.Add(2 * time.Hour))}, nil
				},
				listEscalatedByQuery: map[string][]connectorIssue{defaultDedupQuery: {open}},
			}
			if err := tick(t, opts, spy, t0.Add(3*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if len(spy.created) != 0 {
				t.Fatalf("re-firing fingerprint filed a new bead: %+v", spy.created)
			}
			if len(spy.commented) != 1 || spy.commented[0].id != "zr-open" {
				t.Fatalf("want exactly one comment on zr-open, got %+v", spy.commented)
			}
		})
	}
}

// The same run: two firings of one fingerprint (two episodes) file ONE bead
// and comment on it, because the bead just created is visible to the second.
func TestSameFingerprintTwiceInOneRunFilesOneBead(t *testing.T) {
	opts := episodeOpts(t)
	spy := &spyDeps{
		fetchAlertsFn: func(ctx context.Context, opts runOptions) ([]grafanaAlert, error) {
			return []grafanaAlert{fpAlert(started), fpAlert(started.Add(time.Hour))}, nil
		},
		listEscalatedByQuery: map[string][]connectorIssue{},
	}
	if err := tick(t, opts, spy, t0); err != nil {
		t.Fatal(err)
	}
	if len(spy.created) != 1 {
		t.Fatalf("want one bead for one fingerprint, got %d: %+v", len(spy.created), spy.created)
	}
	if len(spy.commented) != 1 || !strings.Contains(spy.commented[0].body, "still firing") {
		t.Fatalf("want one episode comment on the new bead, got %+v", spy.commented)
	}
}

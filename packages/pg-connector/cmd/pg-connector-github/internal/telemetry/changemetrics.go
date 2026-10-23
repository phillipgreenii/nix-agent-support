package telemetry

import (
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/classify"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/changemetrics"
)

// The instruments of the change engine and the change feed live in
// pkg/connectord/changemetrics, registered by New under metricPrefix, so their
// names are the Metric* constants of telemetry.go. This file adapts them to the
// daemon's observer wiring.

// NewChangeObserver returns the changes.Observer that feeds change.settle and
// change.detection_delay_seconds from the change engine's events. A nil
// Telemetry (the daemon runs on when telemetry fails to start) yields an
// observer that does nothing.
func NewChangeObserver(t *Telemetry) changes.Observer {
	if t == nil {
		return noopChangeObserver{}
	}
	return t.changes.ChangeObserver()
}

// NewFeedObserver returns the feed.Observer that feeds consumer.redeliveries
// from the change feed's events. A nil Telemetry yields an observer that does
// nothing.
func NewFeedObserver(t *Telemetry) feed.Observer {
	if t == nil {
		return noopFeedObserver{}
	}
	return t.changes.FeedObserver()
}

type noopChangeObserver struct{}

func (noopChangeObserver) Detected(string, string, time.Duration) {}
func (noopChangeObserver) Settled(string)                         {}

type noopFeedObserver struct{}

func (noopFeedObserver) Redelivered(string, string, int) {}

// changeKindLabel passes a kind from classify.Kinds() and folds anything else
// to "unknown".
func changeKindLabel(kind string) string {
	return changemetrics.KindLabel(classify.Kinds(), kind)
}

// changeOriginLabel passes one of the four origin words and folds anything else
// to "unknown".
func changeOriginLabel(origin string) string { return changemetrics.OriginLabel(origin) }

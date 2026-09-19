// Package telemetry resolves per-session OTel attributes on demand
// (pg2-24f89/pg2-qye99 D8.2). This file provides SessionAttrs alone; the
// telemetry.Init/shutdown/slog-Fanout wiring lives in a sibling packet and is
// out of scope here.
package telemetry

import (
	"sort"
	"sync"

	"go.opentelemetry.io/otel/attribute"
)

// sessionLabeler is the minimal store surface SessionAttrs needs: resolving
// one session's currently-marked labels into a map (pg2-24f89/pg2-qye99
// D8.1's store.Labels). Declared as an interface — rather than depending on
// *store.Store directly — purely so tests can substitute degraded/failure
// scenarios (an unreachable store) without standing up a real database; the
// production wiring (whoever calls SetSessionLabeler with the real *Store)
// is this packet's own freedom-boundary choice per the packet's Binding
// decisions, since the design fixes only SessionAttrs's own signature and
// its degraded-empty-slice behavior, not how it reaches the store.
type sessionLabeler interface {
	Labels(externalID string) (map[string]string, error)
}

var (
	labelerMu sync.RWMutex
	labeler   sessionLabeler
)

// SetSessionLabeler wires the store SessionAttrs resolves labels from. It is
// called once at startup (by whatever wires the telemetry package's Init,
// per the sibling core packet). Passing nil clears it, making SessionAttrs
// degrade to always returning an empty slice — the same degraded outcome as
// an unreachable store.
func SetSessionLabeler(l sessionLabeler) {
	labelerMu.Lock()
	defer labelerMu.Unlock()
	labeler = l
}

// SessionAttrs resolves externalID's currently-marked session labels
// (store.Labels) into OTel attributes, on demand (pg2-24f89/pg2-qye99 D8.2).
// Attachment is per-record, not per-process: a fixed resource attribute
// cannot correctly represent one session's role/pool for a command like
// reap-all that touches several roles/pools in one invocation, so callers
// resolve fresh per session (once for a single-session call site, once per
// iteration for a multi-session loop) rather than caching this across
// sessions.
//
// A missing/unreachable store (no labeler wired, or the lookup itself
// erroring) and a session with no marked labels are both degraded-but-safe
// telemetry outcomes, never a failure ccpool should propagate: SessionAttrs
// returns an empty (non-nil) slice, never an error, in either case.
//
// store.Labels already returns only the subset of metadata marked as a
// label (D8.1); SessionAttrs converts every entry of that already-filtered
// map to an attribute and applies no further key filtering of its own. Keys
// are emitted in sorted order for deterministic output.
func SessionAttrs(externalID string) []attribute.KeyValue {
	labelerMu.RLock()
	l := labeler
	labelerMu.RUnlock()

	if l == nil {
		return []attribute.KeyValue{}
	}

	labels, err := l.Labels(externalID)
	if err != nil || len(labels) == 0 {
		return []attribute.KeyValue{}
	}

	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	attrs := make([]attribute.KeyValue, 0, len(keys))
	for _, k := range keys {
		attrs = append(attrs, attribute.String(k, labels[k]))
	}
	return attrs
}

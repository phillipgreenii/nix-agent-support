package nudger

import "time"

// Queue adds a manual intent for each sid (idempotent on key). Text is
// the message to send. Selector expansion (path:/cmux:/session:) lives in
// the gRPC handler — Queue takes already-expanded session IDs.
func (p *ManualProducer) Queue(store *PendingStore, sids []string, text string, now time.Time) {
	for _, sid := range sids {
		store.Add(NudgeIntent{
			Key:       IntentKey{SessionID: sid, Source: SourceManual},
			Text:      text,
			EmittedAt: now,
		})
	}
}

// Cancel removes the manual intent for each sid.
func (p *ManualProducer) Cancel(store *PendingStore, sids []string) {
	for _, sid := range sids {
		store.Cancel(IntentKey{SessionID: sid, Source: SourceManual})
	}
}

// Reconcile is a no-op for manual: manual intents persist until either
// the dispatcher fires or the user explicitly cancels.
func (p *ManualProducer) Reconcile(TickContext, *PendingStore) {}

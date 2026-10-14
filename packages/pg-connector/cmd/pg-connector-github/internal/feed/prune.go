package feed

import "context"

// Prune applies change_log_retention to the log and consumer_stale_after to the
// consumer table: it deletes the rows older than Retention and the keys unseen
// for ConsumerStaleAfter, and returns how many of each. A key whose position
// fell out of retention is NOT deleted here: it stays until it is polled and
// answered cursor_expired:.
func (f *Feed) Prune(ctx context.Context) (rows, keys int64, err error) {
	now := f.cfg.Clock.Now()
	if rows, err = f.cfg.Store.PruneChangesBefore(ctx, now.Add(-f.cfg.Retention)); err != nil {
		return 0, 0, err
	}
	if keys, err = f.cfg.Store.DeleteConsumersSeenBefore(ctx, now.Add(-f.cfg.ConsumerStaleAfter)); err != nil {
		return rows, 0, err
	}
	if keys > 0 {
		if err := f.dropStaleHanded(ctx); err != nil {
			return rows, keys, err
		}
	}
	return rows, keys, nil
}

// dropStaleHanded forgets the redelivery bookkeeping of keys that no longer
// exist.
func (f *Feed) dropStaleHanded(ctx context.Context) error {
	stored, err := f.cfg.Store.ListConsumers(ctx)
	if err != nil {
		return err
	}
	live := make(map[keyID]bool, len(stored))
	for _, c := range stored {
		live[keyID{c.Consumer, c.Kind, c.Query}] = true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.handed {
		if !live[k] {
			delete(f.handed, k)
		}
	}
	return nil
}

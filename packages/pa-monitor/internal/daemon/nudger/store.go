package nudger

import "sync"

// PendingStore is a thread-safe map of pending nudge intents keyed by
// (session, source). Mutations are persisted by the caller (nudger
// package's persistence layer) — the store itself is in-memory.
type PendingStore struct {
	mu      sync.Mutex
	intents map[IntentKey]NudgeIntent
}

func NewPendingStore() *PendingStore {
	return &PendingStore{intents: map[IntentKey]NudgeIntent{}}
}

// Add stores the intent. Returns true if the key was newly inserted,
// false if it already existed (the existing entry is left unchanged).
func (s *PendingStore) Add(in NudgeIntent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.intents[in.Key]; ok {
		return false
	}
	s.intents[in.Key] = in
	return true
}

// Cancel removes the intent for key. No-op if absent.
func (s *PendingStore) Cancel(key IntentKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.intents, key)
}

// ClearSession removes all intents (across all sources) for sid.
func (s *PendingStore) ClearSession(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.intents {
		if k.SessionID == sid {
			delete(s.intents, k)
		}
	}
}

// HasAny reports whether any intent is currently pending for sid.
func (s *PendingStore) HasAny(sid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.intents {
		if k.SessionID == sid {
			return true
		}
	}
	return false
}

// SourcesFor returns the sources that currently have a pending intent
// for sid. Order is unspecified.
func (s *PendingStore) SourcesFor(sid string) []Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Source
	for k := range s.intents {
		if k.SessionID == sid {
			out = append(out, k.Source)
		}
	}
	return out
}

// RemoveKeys atomically deletes the specifically-observed keys from the
// store. Unlike ClearSession (which removes all keys for a sid including
// any added after the initial List snapshot), RemoveKeys only removes the
// exact keys that were seen before Dispatch began, so concurrently-added
// intents (e.g. a manual NudgeQueue RPC racing with Dispatch) survive.
func (s *PendingStore) RemoveKeys(keys []IntentKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.intents, k)
	}
}

// List returns a snapshot of all pending intents. Order is unspecified;
// callers that need stable ordering must sort.
func (s *PendingStore) List() []NudgeIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]NudgeIntent, 0, len(s.intents))
	for _, v := range s.intents {
		out = append(out, v)
	}
	return out
}

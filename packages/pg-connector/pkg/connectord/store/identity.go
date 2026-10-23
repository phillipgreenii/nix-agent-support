package store

import (
	"context"
	"errors"
)

// StoreIDKey is the store_meta key that holds the store's identity: 32 lowercase
// hex characters written by ladder rung 4.
const StoreIDKey = "store_id"

// ErrNoIdentity means the store has no identity row. Rung 4 writes it, so a
// store opened by Open always has one; the error says a caller bypassed Open or
// the row was deleted by hand.
var ErrNoIdentity = errors.New("store: the store has no identity (store_id)")

// Identity returns the store's persisted identity. A store moved aside and
// recreated is a new file that climbs the ladder again, so it gets a new
// identity; a store restored from a backup keeps the identity of the copy.
func (s *Store) Identity(ctx context.Context) (string, error) {
	v, ok, err := s.GetMeta(ctx, StoreIDKey)
	if err != nil {
		return "", err
	}
	if !ok || v == "" {
		return "", ErrNoIdentity
	}
	return v, nil
}

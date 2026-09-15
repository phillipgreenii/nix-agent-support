// Package kvstore defines the Store interface INTF-STORE needs and provides
// its default in-memory implementation.
//
// Store is distinct BY PACKAGE AND BY NAME from internal/eventqueue's own
// Store (the write-ahead-log durability store): the two are unrelated, share
// no symbol, and neither package imports the other.
package kvstore

// Store is a simple key/value store: get, put, delete on string keys and
// string values.
//
// Get on a missing key returns ok=false and a nil error — that, not an
// error, is how "absent" is represented. One layer up, INTF-STORE maps
// ok=false to the wire's { id, value: null } absent-reply shape (Task 6.7's
// concern, not this package's).
//
// Delete of a key that is not present is a no-op, not an error.
//
// Implementations MUST be safe for concurrent use by multiple goroutines.
type Store interface {
	Get(key string) (value string, ok bool, err error)
	Put(key, value string) error
	Delete(key string) error
}

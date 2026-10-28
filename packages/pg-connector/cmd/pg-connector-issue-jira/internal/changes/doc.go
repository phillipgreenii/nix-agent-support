// Package changes is the Jira daemon's registration with the shared change
// engine: the registry.Spec (Spec), the engine.Classifier that adapts the pure
// rules of classify and groups, and the thin facade (New, Engine) that builds the
// engine and exposes the one seam the summary persister calls inside its store
// transaction (DetectSummary).
//
// # Emission and delivery
//
// The registration sets registry.EmitWatchedOnly and registry.DeliverByWatched
// explicitly (they are the only modes): the engine
// appends a row only for an issue whose watched flag is set in the same
// transaction, and a row is delivered by that stored flag. An unwatched issue
// gets no row, no pending row and no version bump.
//
// The entered_query and left_query words are named as roles but are not stored
// rows (Membership.StoreRows is false). The arrival of a never-seen issue becomes
// its first-observation added row through the engine's watch path.
//
// # Settling
//
// The settle rule, baselines and the feed are the shared engine's, with the
// summary and detail groups as the awaited groups: a change to the summary
// queues the detail group and is held as a pending row until the group is
// refreshed or the settle timeout passes, so a row describes data already in the
// store.
//
// # Dependencies
//
// The package takes plain values and imports only the sibling groups, classify
// and config packages and the shared library.
package changes

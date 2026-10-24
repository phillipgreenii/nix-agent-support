// Package connectord is the shared library for a pg-connector daemon: the
// generic parts a stateful connector that runs a long-lived daemon builds on
// (store primitives and migrations, scheduler, governor, change log, socket
// server and client, process scaffolding), as subpackages such as store,
// governor, scheduler, socket and proc.
//
// Only the generic parts live here. Everything specific to one upstream
// (field groups, fetchers, classifier, query shapes) stays inside the
// connector binary's own cmd/<binary>/internal/ tree. A connector that adopts
// this library MUST scope its daemon to one upstream rate-limit domain, not to
// one connector type.
//
// The change feed and its group refresh service are shared too. A connector
// supplies its vocabulary as data and its rules through small interfaces; the
// shared packages never switch on a concrete kind:
//
//   - registry holds Spec, the vocabulary of one connector (entity kind, groups,
//     change kinds and roles, views, and the Emission and Delivery modes).
//   - engine is the change engine: it classifies refreshed content through the
//     connector's Classifier, settles pending changes into the log, and sets the
//     watched flag (Watch, WatchOne).
//   - query is the query runner over the connector's own Lister, Aliases,
//     Settings and Tracker; it stores each run's members and marks watched ones.
//   - feed is the change feed: Poll, Ack, Positions and Prune over one cursor per
//     consumer, bound to a view and an optional scope, with the delivery rule of
//     the registration's mode (DeliverByMembership or DeliverByWatched).
//   - feedop serves the feed over the daemon's socket (the changes and
//     changes_ack handlers, and WatchArg for a read that asks to watch).
//   - groupkit holds the group freshness table and the generation-checked write
//     helpers, and groupsvc is the group refresh service built on it (New,
//     Register for non-primary groups, Ensure, Rebuild); the primary group stays
//     a scheduler task of the connector's own.
//   - changemetrics registers the metrics of the feed and engine under the
//     connector's own prefix, and enginetest is a rig with a neutral fake
//     connector for behaviour proofs, groupsvc/groupsvctest being the analogue
//     for the group refresh service.
//   - store carries the ladder of seven rungs, the one place the shared schema
//     grows; the wire types of the answers live in pkg/schema.
//
// The genericity guard keeps the library generic mechanically:
// genericity_guard_test.go in this package scans every Go file under
// pkg/connectord for the known words of one upstream, for string-literal
// comparisons in the generic packages and for imports of a cmd or internal
// tree, against the committed baseline testdata/genericity_allowlist.json. The
// guard's header comment says how to regenerate the baseline counts. Its third
// rule is the import boundary: no file under this tree imports a path containing
// /cmd/ or /internal/, and none declares package main.
//
// This package and its subpackages are part of the module's shared-surface
// allowlist (cmd/pg-connector/layout_convention_test.go): any binary may
// import them, so they MUST NOT import a binary's internal/ tree or package
// main.
package connectord

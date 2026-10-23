// Package groupsvc is the shared group refresh service of a connector daemon:
// the contracts a connector implements so the daemon can fetch, store, serve
// and expire the non-primary groups of its entities.
//
// This file set holds the contracts only (the first of the hoist's five steps):
//
//   - Fetcher, Target, Outcome and CallMeta are what a connector supplies per
//     group: the service hands it batches of Targets and stores what comes back.
//   - KeepAlive, ErrorPolicy, Describer and RefResolver are the small interfaces
//     through which the connector's own rules reach the service: retention of
//     entities nobody queries, the reading of an origin failure, the decoding of
//     the stored primary content, and the first-sighting resolution of a wire
//     reference.
//   - Config names all of them together with the shared store, scheduler, rules
//     and alias table, the sentinels the connector wants returned, and the hooks
//     the change engine attaches to.
//
// The package names no entity kind, group, stamp meaning or origin: the kind and
// groups come from Config.Spec, the primary content is read through
// Config.Describer, and everything else arrives as data or through an
// interface.
package groupsvc

// Package engine is the shared change engine of a connector daemon. It holds
// the result of one classified content change and the operations over it; the
// kinds a result carries arrive as data (a registry.Spec), never as literals of
// this package.
//
// Exported helpers the connector's own classifier uses after the move:
//
//   - Result, Field: one change and one changed field.
//   - EncodeFields, DecodeFields: the stored JSON form of Result.Fields.
//   - Normalize: drop unchanged fields, compact values, sort by name.
//   - OrderKinds: distinct kinds in a given canonical order.
//   - Merge: combine two results of one entity, in a given kind order.
//   - MembershipResult, RemovedResult: the results of the membership and
//     removed rows, with the kinds taken from registry.Roles.
//   - QueryFieldPrefix: the one protocol-level field name the shared code knows.
package engine

// Package targetspec is the P8 TARGET-SPEC data layer (docket tc-o14i5.3,
// packet tc-o14i5.3.4): a spec-kind sibling to the command specs Phase 1
// shipped a loader for (internal/specfmt, packet tc-o14i5.2.1), covering
// docker context/host, kube context/server, vault address, ssh host, and
// git remote entries, each carrying a trust class (trusted-dev/production).
//
// This package does NOT duplicate specfmt's three-layer Repository/merge
// machinery (embedded < user < repo precedence, undeclared-override
// Conflict detection, fail-closed exclusion of an Invalid spec) — v1.go,
// families.go and validate.go in internal/specfmt already extended that
// SAME loader to understand KindTarget specs (Spec.Target, MergedSet.Targets)
// alongside the KindCommand specs it already carried; specfmt.Repository was
// already generic enough (Load()'s merge loop keys on (Kind, Name, SubKind)
// regardless of what the spec IS) to carry a second populated kind without
// any parallel loader being written.
//
// Repository here is a thin, domain-specific wrapper: NewRepository builds
// one exactly like specfmt.NewRepository does, and Load returns a Lookup —
// the "(name, kind) -> (class, found bool)" API this packet's Contract
// promises K10 (push-policy) — instead of handing back the raw
// specfmt.MergedSet, so a caller never needs to know the merge machinery's
// own TargetKey/map shape to ask "is this target listed, and what class".
//
// The mutation VERDICT (production => Reject, unlisted => Abstain,
// trusted-dev => defer to the effect itself) is deliberately NOT computed
// here — P14 ("specs are facts, not policy; verdicts come only from engine
// POLICY, never from a spec value directly") places that in
// internal/effectpolicy.TargetSpecPolicy, the one reader of a Lookup's
// Class result.
package targetspec

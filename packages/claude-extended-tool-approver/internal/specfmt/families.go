package specfmt

// verbFamilies and remoteFamilies are this package's own small registries
// for CommandSpecV1.VerbFamily and ImplicitEffectV1.RemoteFamily — cmddesc
// has no equivalent registry for these two (unlike Interpreter and Dialect,
// which cmddesc.LookupInterpreter/LookupDialect already validate), because
// they are free-form strings stamped onto Effect.Family rather than names
// resolved against a lookup table at interpretation time. Seeded from the
// values actually used in internal/cmddesc/registry_breadth.go as of
// 2026-09-29 (VerbFamily: "just", "npm", "devbox", "nix"; RemoteFamily:
// "kubectl"). The empty string always means "no family" and is never
// rejected — see IsKnownVerbFamily/IsKnownRemoteFamily.
//
// RegisterVerbFamily/RegisterRemoteFamily mirror cmddesc's own
// RegisterDialect/RegisterInterpreter convention: a later packet (e.g. one
// marshalling a new cmddesc registry entry that introduces a family this
// package does not yet know) widens the set by calling one of these, rather
// than this package needing to import registry_breadth.go or duplicate its
// schema data.
var (
	verbFamilies = map[string]bool{
		"just":   true,
		"npm":    true,
		"devbox": true,
		"nix":    true,
	}
	remoteFamilies = map[string]bool{
		"kubectl": true,
	}
)

// IsKnownVerbFamily reports whether name is a registered VerbFamily, or is
// empty (no family named at all).
func IsKnownVerbFamily(name string) bool {
	return name == "" || verbFamilies[name]
}

// RegisterVerbFamily adds name to the known VerbFamily set. It exists for
// tests and extension; it is not safe to call concurrently with Validate or
// Repository.Load.
func RegisterVerbFamily(name string) {
	verbFamilies[name] = true
}

// IsKnownRemoteFamily reports whether name is a registered RemoteFamily, or
// is empty (the pre-existing, generic RemoteMutation policy routing — see
// cmddesc.ImplicitEffect.RemoteFamily's doc comment).
func IsKnownRemoteFamily(name string) bool {
	return name == "" || remoteFamilies[name]
}

// RegisterRemoteFamily adds name to the known RemoteFamily set. It exists
// for tests and extension; it is not safe to call concurrently with
// Validate or Repository.Load.
func RegisterRemoteFamily(name string) {
	remoteFamilies[name] = true
}

package targetspec

import (
	"io/fs"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
)

// Re-exported vocabulary: a caller of this package should not need to import
// internal/specfmt directly just to name a TargetKind/TargetClass — see this
// package's own doc comment for why the FORMAT lives in specfmt and the
// LOOKUP/POLICY-FACING API lives here.
type (
	// TargetKind is specfmt.TargetKind (see its own doc comment).
	TargetKind = specfmt.TargetKind
	// TargetClass is specfmt.TargetClass (see its own doc comment).
	TargetClass = specfmt.TargetClass
)

const (
	TargetKindDockerContext = specfmt.TargetKindDockerContext
	TargetKindDockerHost    = specfmt.TargetKindDockerHost
	TargetKindKubeContext   = specfmt.TargetKindKubeContext
	TargetKindKubeServer    = specfmt.TargetKindKubeServer
	TargetKindVaultAddress  = specfmt.TargetKindVaultAddress
	TargetKindSSHHost       = specfmt.TargetKindSSHHost
	TargetKindGitRemote     = specfmt.TargetKindGitRemote

	TargetClassTrustedDev = specfmt.TargetClassTrustedDev
	TargetClassProduction = specfmt.TargetClassProduction
)

// Repository loads P8 target specs from the three specfmt layers
// (embedded/user/repo — see specfmt.Repository's own doc comment for the
// precedence rule) and reduces them to a Lookup. It wraps a
// *specfmt.Repository rather than reimplementing loadLayer/merge — see this
// package's own doc comment.
type Repository struct {
	inner *specfmt.Repository
}

// NewRepository builds a Repository over the three layers, exactly mirroring
// specfmt.NewRepository's own parameter shape and nil/empty-layer handling:
// a nil embedded fs.FS, or a userDir/repoDir that does not exist, is an
// empty layer, not an error.
func NewRepository(embedded fs.FS, userDir, repoDir string) *Repository {
	return &Repository{inner: specfmt.NewRepository(embedded, userDir, repoDir)}
}

// DefaultUserDir and DefaultRepoDir are specfmt's own conventional
// directories (see their doc comments) — re-exported here so a caller
// wiring a target-spec Repository does not also need to import specfmt
// directly for these two constructors.
var (
	DefaultUserDir = specfmt.DefaultUserDir
	DefaultRepoDir = specfmt.DefaultRepoDir
)

// Load reads and merges all three layers and returns the resulting Lookup
// plus specfmt's own Conflict/InvalidSpec diagnostics (see
// specfmt.MergedSet's doc comment for what each means) — Load does not
// swallow either, since a caller (a future linter, exactly like packet
// 1.3's command-spec linter) may want to turn an undeclared override or an
// excluded invalid spec into a hard failure.
func (r *Repository) Load() (*Lookup, []specfmt.Conflict, []specfmt.InvalidSpec, error) {
	merged, err := r.inner.Load()
	if err != nil {
		return nil, nil, nil, err
	}
	classes := make(map[specfmt.TargetKey]specfmt.TargetClass, len(merged.Targets))
	for k, t := range merged.Targets {
		classes[k] = t.Class
	}
	return &Lookup{targets: classes}, merged.Conflicts, merged.Invalid, nil
}

// Lookup is the target-spec "Repository/lookup API" this packet's Contract
// Produces for K10 (push-policy) to consume: (name, kind) -> (class, found
// bool). It is a read-only snapshot of one Repository.Load() call — nothing
// here re-reads disk.
type Lookup struct {
	targets map[specfmt.TargetKey]specfmt.TargetClass
}

// Class reports the operator-declared TargetClass for (name, kind), and
// whether the target is listed at all. A nil Lookup (the zero value of
// *Lookup — no Repository has ever been loaded) always reports !found,
// exactly like an empty one: "no configuration loaded" and "loaded, but
// this target is not in it" are indistinguishable to a caller by design —
// both mean the same P8 verdict, "unlisted target" (Abstain).
func (l *Lookup) Class(name string, kind TargetKind) (TargetClass, bool) {
	if l == nil {
		return "", false
	}
	class, ok := l.targets[specfmt.TargetKey{Kind: kind, Name: name}]
	return class, ok
}

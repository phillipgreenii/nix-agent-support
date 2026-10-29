package specfmt

import (
	"fmt"
	"sort"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
)

// SkipReason says why AssembleRegistry left one loaded command spec out.
type SkipReason string

const (
	// SkipBuiltinName: the spec's name is already a built-in schema. Per the
	// pg2-7ekzoi ruling a user/repo spec may only ADD a command, never
	// replace one, even with Overrides=true (see AssembleRegistry).
	SkipBuiltinName SkipReason = "builtin-name"
	// SkipConflict: two layers supplied the same name and the later one did
	// not declare Overrides (MergedSet.Conflicts); neither is trusted.
	SkipConflict SkipReason = "conflict"
	// SkipConvert: ToSchema rejected the spec, or its schema name differs
	// from the spec name.
	SkipConvert SkipReason = "convert"
)

// SkippedSpec records one command spec AssembleRegistry excluded.
type SkippedSpec struct {
	Name   string
	Reason SkipReason
	Err    error
}

// AssembleRegistry returns builtin plus the command specs in set, as a new
// cmddesc.Registry. builtin is never mutated. It is the one place the live
// hook's registry is composed from the user and repo spec layers (ADR 0075,
// P17), and every rule below fails CLOSED, because a spec can only turn an
// Abstain into an Approve:
//
//   - Invalid specs are already absent from set.Commands (Repository.Load
//     records them in set.Invalid); they never reach the registry.
//   - ADD-ONLY (pg2-7ekzoi design ruling): a spec whose name is a built-in
//     schema is skipped even when it declares Overrides. Overrides keeps its
//     meaning between the user and repo layers (the repo spec then wins), but
//     it cannot loosen a built-in: a repo's `.ceta/` is agent-writable
//     territory by default, so letting it redefine `git` or `rm` would hand
//     an agent the policy. Replacing a built-in needs a code change.
//   - A name in set.Conflicts (undeclared override between layers) is
//     skipped entirely rather than picking a winner.
//   - A spec ToSchema rejects is skipped.
func AssembleRegistry(builtin cmddesc.Registry, set *MergedSet) (cmddesc.Registry, []SkippedSpec) {
	if set == nil {
		return builtin, nil
	}
	conflicted := map[string]bool{}
	for _, c := range set.Conflicts {
		if c.Kind == KindCommand {
			conflicted[c.Name] = true
		}
	}
	names := make([]string, 0, len(set.Commands))
	for n := range set.Commands {
		names = append(names, n)
	}
	sort.Strings(names)

	var schemas []cmddesc.CommandSchema
	for _, n := range builtin.Names() {
		s, _ := builtin.Lookup(n)
		schemas = append(schemas, s)
	}
	var skipped []SkippedSpec
	for _, n := range names {
		if _, ok := builtin.Lookup(n); ok {
			skipped = append(skipped, SkippedSpec{Name: n, Reason: SkipBuiltinName})
			continue
		}
		if conflicted[n] {
			skipped = append(skipped, SkippedSpec{Name: n, Reason: SkipConflict})
			continue
		}
		s, err := ToSchema(set.Commands[n])
		if err == nil && s.Name != n {
			err = fmt.Errorf("schema name %q differs from spec name %q", s.Name, n)
		}
		if err != nil {
			skipped = append(skipped, SkippedSpec{Name: n, Reason: SkipConvert, Err: err})
			continue
		}
		schemas = append(schemas, s)
	}
	return cmddesc.NewRegistry(schemas...), skipped
}

// LoadLiveRegistry loads the user and repo layers (an empty dir is an empty
// layer) and assembles them over builtin. userDir SHOULD come from
// DefaultUserDir (HOME-resolved, ADR 0075 P7). A load error (unreadable dir,
// malformed JSON) fails closed: builtin is returned unchanged with the error,
// so the caller can log it and keep running on built-ins only.
func LoadLiveRegistry(builtin cmddesc.Registry, userDir, repoDir string) (cmddesc.Registry, *MergedSet, []SkippedSpec, error) {
	set, err := NewRepository(nil, userDir, repoDir).Load()
	if err != nil {
		return builtin, nil, nil, err
	}
	reg, skipped := AssembleRegistry(builtin, set)
	return reg, set, skipped, nil
}

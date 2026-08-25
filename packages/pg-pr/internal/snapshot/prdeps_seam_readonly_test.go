package snapshot

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/prdeps"
)

// forbiddenSeamPkgFragments name import-path fragments that would mean the
// PR-dependency graph pass Build calls (builder.go's
// `prdeps.DeriveWithNativeStack(prdeps.Input{...})`) gained a store handle, a
// provider/network client, or a subprocess -- exactly what INV-READ-1 (no
// network call, no store mutation) and INV-SYNC-1 (change detection mutates
// nothing), docs/behavior/invariants.md, forbid it from ever having.
var forbiddenSeamPkgFragments = []string{
	"/internal/store",
	"/provider/",
	"net/http",
	"os/exec",
	"database/sql",
}

// TestGraphPassSeamTakesNoInjectableDependency is the store/network half of
// bead pg2-4dz88.3.8's guard for pg-pr's stack-identification feature (the
// other half, the gh-stack-mutating-verb source scan, lives in
// pkg/provider/vcs/github/stack_readonly_test.go).
//
// # Why this is a structural/signature test, not a fake-injection test
//
// The bead's own design field describes injecting, at this seam, "a store
// fake whose every write method calls t.Fatal, and a provider fake that
// t.Fatal's on any invocation the test did not stage" -- the pattern a
// dependency-injection seam with a real interface parameter would call for.
// This seam does not have one:
//
//   - Build (builder.go:186) calls prdeps.DeriveWithNativeStack (builder.go
//     ~210) with prdeps.Input{PRs: toPRDeps(in.PRs), TrunkRefs: in.TrunkRefs}
//     -- toPRDeps projects already-fetched PRInput VALUES into prdeps.PR
//     VALUES; nothing live is threaded through.
//   - internal/prdeps's own non-test source imports ONLY the standard
//     library (see native.go, prdeps.go), a fact internal/prdeps's own
//     TestPackageImportsStdlibOnly already enforces by source-scanning that
//     package's imports. DeriveWithNativeStack is consequently a pure
//     values-in/values-out function: there is no store handle, no provider
//     handle and no network client anywhere in its scope for a t.Fatal-on-
//     unstaged-call fake to stand in for.
//
// Inventing a store or provider PARAMETER on Build or DeriveWithNativeStack
// purely so a fake would have somewhere to plug in would test a shape
// production code does not have, and give false confidence -- exactly the
// failure mode this bead's own instructions warn against. The mechanical
// equivalent that fits what is actually there is to assert, by construction,
// that the seam's real signature carries nothing store/provider/client-shaped
// -- the same ENFORCEMENT MECHANISM as the sibling gh-stack source scan
// (walk real structure, fail on a forbidden shape), aimed at a different
// target: the exact function the seam calls, rather than a directory of
// source files.
//
// This complements, rather than duplicates, internal/prdeps's
// TestPackageImportsStdlibOnly: that test proves the PACKAGE never imports a
// store/provider/network path (it lives beside the code it protects, in
// internal/prdeps). This test proves the exact SIGNATURE builder.go calls at
// the seam is reachable from nothing but plain value types, from the seam's
// own side (internal/snapshot, the package that owns the call site) -- and it
// also catches a shape the import scan cannot: a same-package interface{}-
// shaped parameter that a fake could satisfy without ever importing a
// forbidden package at all. Reflection is used, per the bead's own suggested
// design, to walk the actual reachable type graph rather than trust a
// snapshot of it.
func TestGraphPassSeamTakesNoInjectableDependency(t *testing.T) {
	fn := reflect.TypeOf(prdeps.DeriveWithNativeStack)
	if fn.Kind() != reflect.Func {
		t.Fatalf("reflect.TypeOf(prdeps.DeriveWithNativeStack) is not a func (got %s) -- "+
			"the seam's call shape changed; update this test to match it", fn.Kind())
	}

	seen := make(map[reflect.Type]bool)
	visited := 0
	var offenders []string

	inspect := func(rt reflect.Type) {
		id := rt.String()
		if rt.Kind() == reflect.Interface {
			offenders = append(offenders, fmt.Sprintf(
				"%s is an interface type -- an interface is exactly the shape a store/provider fake would be handed through", id,
			))
			return
		}
		path := rt.PkgPath()
		if path == "" {
			return
		}
		for _, frag := range forbiddenSeamPkgFragments {
			if strings.Contains(path, frag) {
				offenders = append(offenders, fmt.Sprintf("%s comes from package %q", id, path))
			}
		}
	}

	var walk func(rt reflect.Type)
	walk = func(rt reflect.Type) {
		if rt == nil || seen[rt] {
			return
		}
		seen[rt] = true
		visited++
		inspect(rt)
		switch rt.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
			walk(rt.Elem())
		case reflect.Map:
			walk(rt.Key())
			walk(rt.Elem())
		case reflect.Struct:
			for i := 0; i < rt.NumField(); i++ {
				walk(rt.Field(i).Type)
			}
		case reflect.Func:
			for i := 0; i < rt.NumIn(); i++ {
				walk(rt.In(i))
			}
			for i := 0; i < rt.NumOut(); i++ {
				walk(rt.Out(i))
			}
		}
	}

	for i := 0; i < fn.NumIn(); i++ {
		walk(fn.In(i))
	}
	for i := 0; i < fn.NumOut(); i++ {
		walk(fn.Out(i))
	}

	if visited == 0 {
		t.Fatal("walked no types from DeriveWithNativeStack's signature; the guard proved nothing")
	}
	if len(offenders) > 0 {
		t.Fatalf("the graph-pass seam (prdeps.DeriveWithNativeStack, called from snapshot.Build) "+
			"is reachable from a store/provider/network-shaped type:\n  %s\n\n"+
			"The PR-dependency pass must stay a pure values-in/values-out function -- INV-READ-1 "+
			"and INV-SYNC-1, docs/behavior/invariants.md -- so nothing store/provider/client-shaped "+
			"may enter its signature.",
			strings.Join(offenders, "\n  "))
	}
}

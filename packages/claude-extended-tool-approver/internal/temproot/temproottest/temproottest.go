// Package temproottest is the shared TEST helper for every test that builds a
// fixture under t.TempDir() and then expects internal/temproot to classify it
// as a temporary root (pg2-gg2bf).
//
// WHY: temproot.Roots() is deliberately a closed, configuration-only list
// (the realpath of $TMPDIR, /private/var/folders, /private/tmp, /tmp, plus
// CETA_EXTRA_TEMP_ROOTS; ADR 0059). Where t.TempDir() lands is NOT part of
// that list: since Go 1.26 it honours GOTMPDIR, so a shell that exports
// GOTMPDIR=/Volumes/gitrepos/build_tmp (the build-tmp home-manager module)
// put every fixture outside every built-in root and every "relaxed under a
// temp root" case was refused. A test therefore MUST declare its own fixture
// root rather than rely on where the caller's environment puts temp files.
// Production code does NOT read GOTMPDIR and MUST NOT: this package changes
// no production behaviour, it only configures the test process.
package temproottest

import (
	"os"
	"testing"
)

// extraRootsEnvVar mirrors the unexported name in internal/temproot; the
// regression guard in this package fails if the two ever drift apart.
const extraRootsEnvVar = "CETA_EXTRA_TEMP_ROOTS"

// TempDir returns a fresh t.TempDir() and declares it a temp root for the
// test's duration: it is appended (":"-separated, merged with any value
// already present, never replacing it) to CETA_EXTRA_TEMP_ROOTS through
// t.Setenv, which restores the previous value on cleanup. It is
// the drop-in replacement for t.TempDir() in a test whose assertion depends
// on the fixture counting as temporary.
//
// Because it uses t.Setenv it MUST NOT be called from a test that (or whose
// ancestor) called t.Parallel.
//
// It is for POSITIVE (carve-out) fixtures only. A negative case (a real,
// non-temp GIT_DIR or -C, alone or mixed with a temp one) MUST keep naming a
// genuinely non-temp path, never a value from this helper.
func TempDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	DeclareRoot(t, dir)
	return dir
}

// DeclareRoot adds dir to CETA_EXTRA_TEMP_ROOTS for the test's duration,
// merging with any existing value. Use it when the fixture directory is made
// some other way than TempDir.
func DeclareRoot(t testing.TB, dir string) {
	t.Helper()
	merged := dir
	if existing := os.Getenv(extraRootsEnvVar); existing != "" {
		merged = existing + ":" + dir
	}
	t.Setenv(extraRootsEnvVar, merged)
}

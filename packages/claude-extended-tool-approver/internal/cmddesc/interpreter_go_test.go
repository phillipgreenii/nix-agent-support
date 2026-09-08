package cmddesc

import (
	"reflect"
	"strings"
	"testing"
)

// TestGoIsRegistryOnlySubcommand: the same proof
// TestGitLogIsRegistryOnlySubcommand (interpreter_subcommand_test.go) gives
// for git log — goSchema's "test" subcommand resolves through the SAME
// interpretSubcommand code path as every other Subcommands entry, generic
// interpreter, no branch on the name "go" or "test" anywhere.
func TestGoIsRegistryOnlySubcommand(t *testing.T) {
	reg := DefaultRegistry()
	goSchema, ok := reg.Lookup("go")
	if !ok {
		t.Fatal("go not registered")
	}
	testSchema, ok := goSchema.Subcommands["test"]
	if !ok {
		t.Fatal("go test not registered as a subcommand")
	}
	if testSchema.Interpreter != "" {
		t.Fatalf("go test names interpreter %q; must be generic", testSchema.Interpreter)
	}

	in := GenericInterpreter{}
	l := leaf(t, "go test ./...")
	got := in.Interpret(l, goSchema, Context{})
	renamed := goSchema
	renamed.Name = "not-go"
	if again := in.Interpret(l, renamed, Context{}); !reflect.DeepEqual(got, again) {
		t.Errorf("interpreter result depends on schema.Name:\n%+v\n%+v", got, again)
	}
	want := []Effect{
		{Kind: EffectPath, Path: "./...", Access: AccessRead, Source: "arg 0", FromPositional: true},
		{Kind: EffectExec, Detail: "trusted checkout code", Source: "go test"},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want effects %+v", got, want)
	}
}

// TestGoModIsNestedSubcommand: goSchema's "mod" entry is itself a
// Subcommands dispatch (mirrors gitWorktreeSchema/bdSchema's dep/label/dolt
// nesting) — interpretSubcommand recurses one level deeper with no new code.
func TestGoModIsNestedSubcommand(t *testing.T) {
	reg := DefaultRegistry()
	goSchema, ok := reg.Lookup("go")
	if !ok {
		t.Fatal("go not registered")
	}
	got := GenericInterpreter{}.Interpret(leaf(t, "go mod tidy"), goSchema, Context{})
	want := []Effect{
		{Kind: EffectExec, Detail: "trusted checkout code", Source: "go mod tidy"},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestGoRunTargetIsUnmodeled: go run's package/file target resolves to
// KindUnmodeled — visible in the graph (Sufficient reads false, not a parse
// failure) but always insufficient regardless of what the target or its
// trailing arguments are, per the operator ruling recorded on goRunSchema's
// doc comment ("abstoan on it").
func TestGoRunTargetIsUnmodeled(t *testing.T) {
	reg := DefaultRegistry()
	goSchema, ok := reg.Lookup("go")
	if !ok {
		t.Fatal("go not registered")
	}
	in := GenericInterpreter{}

	t.Run("package target", func(t *testing.T) {
		got := in.Interpret(leaf(t, "go run ./cmd/tool"), goSchema, Context{})
		if got.Sufficient || !strings.Contains(got.Insufficiency, "unmodeled operand role") {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("file target plus a flag-shaped program argument", func(t *testing.T) {
		// PositionalsEndOptions stops flag scanning at the target: "--flag"
		// must land as a SECOND positional (the program's own argv), never
		// as an unrecognised go run flag.
		got := in.Interpret(leaf(t, "go run main.go --flag"), goSchema, Context{})
		if got.Sufficient || !strings.Contains(got.Insufficiency, "unmodeled operand role") {
			t.Errorf("got %+v", got)
		}
	})
}

// TestGoInstallGetAlwaysInsufficient: install/get force insufficiency
// UNCONDITIONALLY, via an ImplicitEffect with no WhenNoPositionals/WhenFlags
// condition — including the bare, zero-positional form no golden exercises.
func TestGoInstallGetAlwaysInsufficient(t *testing.T) {
	reg := DefaultRegistry()
	goSchema, ok := reg.Lookup("go")
	if !ok {
		t.Fatal("go not registered")
	}
	in := GenericInterpreter{}
	for _, cmd := range []string{"go install", "go install ./cmd/x", "go get", "go get example.com/m@v1"} {
		got := in.Interpret(leaf(t, cmd), goSchema, Context{})
		if got.Sufficient {
			t.Errorf("%q: expected insufficient, got %+v", cmd, got)
		}
	}
}

// TestGoFlagGluedEquals: go's own single-dash "-flag=value" convention
// (`go help testflag`'s own worked example: "-cpuprofile=prof.out") parses
// as a glued value on the FIRST try, not via getopt-style short-flag
// bundling (which -count/-timeout/-coverprofile — all multi-character names
// — could never satisfy character-by-character).
func TestGoFlagGluedEquals(t *testing.T) {
	reg := DefaultRegistry()
	goSchema, ok := reg.Lookup("go")
	if !ok {
		t.Fatal("go not registered")
	}
	got := GenericInterpreter{}.Interpret(leaf(t, "go test -count=1 ./..."), goSchema, Context{})
	want := []Effect{
		{Kind: EffectPath, Path: "./...", Access: AccessRead, Source: "arg 1", FromPositional: true},
		{Kind: EffectExec, Detail: "trusted checkout code", Source: "go test"},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	// A single-dash flag name that is NOT registered still fails closed, "="
	// or not — gluedFlag must never manufacture a match.
	unknown := GenericInterpreter{}.Interpret(leaf(t, "go test -notaflag=1 ./..."), goSchema, Context{})
	if unknown.Sufficient || !strings.Contains(unknown.Insufficiency, "unknown flag") {
		t.Errorf("got %+v", unknown)
	}
}

// TestGoCoverprofileIsPathTruncate: -coverprofile's value is a real
// filesystem write (PathTruncate), the one go-test flag this slice tracks
// by path — the same shape as gofmtSchema's -cpuprofile.
func TestGoCoverprofileIsPathTruncate(t *testing.T) {
	reg := DefaultRegistry()
	goSchema, ok := reg.Lookup("go")
	if !ok {
		t.Fatal("go not registered")
	}
	got := GenericInterpreter{}.Interpret(leaf(t, "go test -coverprofile /nix/store/x ./..."), goSchema, Context{})
	want := []Effect{
		{Kind: EffectPath, Path: "/nix/store/x", Access: AccessTruncate, Source: "arg 1"},
		{Kind: EffectPath, Path: "./...", Access: AccessRead, Source: "arg 2", FromPositional: true},
		{Kind: EffectExec, Detail: "trusted checkout code", Source: "go test"},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestGoCleanCacheFlags: -cache/-modcache each emit an implicit PathDelete of
// their own declared cache root, conditioned on the matching flag (WhenFlags)
// — absent either flag, go clean emits no cache-delete effect at all (the
// narrower, per-package-object-file gap this slice deliberately leaves
// unmodeled, per goCleanSchema's own doc comment).
func TestGoCleanCacheFlags(t *testing.T) {
	reg := DefaultRegistry()
	goSchema, ok := reg.Lookup("go")
	if !ok {
		t.Fatal("go not registered")
	}
	in := GenericInterpreter{}

	cache := in.Interpret(leaf(t, "go clean -cache"), goSchema, Context{})
	wantCache := []Effect{
		{Kind: EffectPath, Path: "~/.cache/go-build", Access: AccessDelete, Source: "implicit"},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !cache.Sufficient || !reflect.DeepEqual(cache.Effects, wantCache) {
		t.Errorf("-cache: got %+v, want %+v", cache, wantCache)
	}

	modcache := in.Interpret(leaf(t, "go clean -modcache"), goSchema, Context{})
	wantModcache := []Effect{
		{Kind: EffectPath, Path: "~/go/pkg/mod", Access: AccessDelete, Source: "implicit"},
		{Kind: EffectStdio, Stream: StreamStdout, Metadata: true},
	}
	if !modcache.Sufficient || !reflect.DeepEqual(modcache.Effects, wantModcache) {
		t.Errorf("-modcache: got %+v, want %+v", modcache, wantModcache)
	}

	bare := in.Interpret(leaf(t, "go clean"), goSchema, Context{})
	wantBare := []Effect{{Kind: EffectStdio, Stream: StreamStdout, Metadata: true}}
	if !bare.Sufficient || !reflect.DeepEqual(bare.Effects, wantBare) {
		t.Errorf("bare `go clean`: got %+v, want %+v (no cache-delete without a cache flag)", bare, wantBare)
	}
}

// TestGoUnmodeledSubcommand: an unregistered go subcommand fails exactly
// like git's own unmodeled subcommands (worktree frobnicate, since slice 3ac
// modeled add/remove/prune/move/lock/unlock/repair) — no special code for
// "doc"/"tool"/"work", the absent map key is enough.
func TestGoUnmodeledSubcommand(t *testing.T) {
	reg := DefaultRegistry()
	goSchema, ok := reg.Lookup("go")
	if !ok {
		t.Fatal("go not registered")
	}
	for _, cmd := range []string{"go doc fmt.Println", "go tool nm", "go work use .", "go frobnicate"} {
		got := GenericInterpreter{}.Interpret(leaf(t, cmd), goSchema, Context{})
		if got.Sufficient || !strings.Contains(got.Insufficiency, "unmodeled subcommand") {
			t.Errorf("%q: got %+v", cmd, got)
		}
	}
}

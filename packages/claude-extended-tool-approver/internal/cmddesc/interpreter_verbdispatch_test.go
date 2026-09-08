package cmddesc

import (
	"reflect"
	"testing"
)

// TestVerbDispatch_Just (tc-8og1 item 3 sub-slice 4; tc-vn5z Q4) covers
// justSchema's VerbFamily dispatch: the recipe positional becomes a single
// EffectExec{Family: "just", Operation: <recipe>}, global flags before it
// are scanned (and an unknown one fails closed), and everything after the
// recipe is opaque — even a flag-shaped trailing token never makes the
// invocation Insufficient, since scanGlobal stops looking for flags at the
// first positional.
func TestVerbDispatch_Just(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("just")
	if !ok {
		t.Fatal("just not registered")
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("no interpreter resolved for just")
	}

	t.Run("bare invocation lists recipes, no dispatch", func(t *testing.T) {
		got := in.Interpret(leaf(t, "just"), schema, Context{})
		want := []Effect{{Kind: EffectStdio, Stream: StreamStdout, Metadata: true}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("verb captured as EffectExec", func(t *testing.T) {
		got := in.Interpret(leaf(t, "just build"), schema, Context{})
		want := []Effect{{Kind: EffectExec, Family: "just", Operation: "build", Source: "arg 0"}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("global flag before verb is scanned, verb still captured", func(t *testing.T) {
		got := in.Interpret(leaf(t, "just --dry-run build"), schema, Context{})
		want := []Effect{{Kind: EffectExec, Family: "just", Operation: "build", Source: "arg 1"}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("trailing flag-shaped argument is opaque, not an unmodeled flag", func(t *testing.T) {
		got := in.Interpret(leaf(t, "just deploy --prod"), schema, Context{})
		want := []Effect{{Kind: EffectExec, Family: "just", Operation: "deploy", Source: "arg 0"}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("unknown global flag before the verb fails closed", func(t *testing.T) {
		got := in.Interpret(leaf(t, "just --frobnicate build"), schema, Context{})
		if got.Sufficient {
			t.Errorf("got Sufficient=true, want an unmodeled global flag to fail the interpretation closed: %+v", got)
		}
	})

	t.Run("dynamic verb captured as a Dynamic EffectExec, still Sufficient", func(t *testing.T) {
		got := in.Interpret(leaf(t, `just "$V"`), schema, Context{})
		want := []Effect{{Kind: EffectExec, Family: "just", Dynamic: true, Source: "arg 0"}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})
}

// TestVerbDispatch_NpmRun proves the SAME VerbFamily mechanism reached
// through the ordinary interpretSubcommand recursion (npm -> "run"), with
// Family stamped "npm" (matching npmKind.Name) rather than the dispatching
// subcommand key "run" (schema.Name) — the reason VerbFamily is a distinct
// field from Name (see VerbFamily's own doc comment, schema.go).
func TestVerbDispatch_NpmRun(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("npm")
	if !ok {
		t.Fatal("npm not registered")
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("no interpreter resolved for npm")
	}

	t.Run("run <verb> captured as EffectExec{Family: npm}", func(t *testing.T) {
		got := in.Interpret(leaf(t, "npm run build"), schema, Context{})
		want := []Effect{{Kind: EffectExec, Family: "npm", Operation: "build", Source: "arg 0"}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("bare run lists scripts, no dispatch", func(t *testing.T) {
		got := in.Interpret(leaf(t, "npm run"), schema, Context{})
		want := []Effect{{Kind: EffectStdio, Stream: StreamStdout, Metadata: true}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("trailing -- args are opaque", func(t *testing.T) {
		got := in.Interpret(leaf(t, "npm run test -- --grep=pattern"), schema, Context{})
		want := []Effect{{Kind: EffectExec, Family: "npm", Operation: "test", Source: "arg 0"}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("an unmodeled npm subcommand stays unmodeled (not this slice's job)", func(t *testing.T) {
		got := in.Interpret(leaf(t, "npm install"), schema, Context{})
		if got.Sufficient {
			t.Errorf("got Sufficient=true for npm install, want unmodeled subcommand insufficiency: %+v", got)
		}
	})
}

// TestVerbDispatch_DevboxRun mirrors TestVerbDispatch_NpmRun for devbox
// (not verified live — see devboxSchema's own doc comment).
func TestVerbDispatch_DevboxRun(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("devbox")
	if !ok {
		t.Fatal("devbox not registered")
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("no interpreter resolved for devbox")
	}
	got := in.Interpret(leaf(t, "devbox run test"), schema, Context{})
	want := []Effect{{Kind: EffectExec, Family: "devbox", Operation: "test", Source: "arg 0"}}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want effects %+v", got, want)
	}
}

// TestVerbDispatch_NixRun (tc-8og1 item 3 sub-slice 5; tc-vn5z Q4) covers
// nixRunSchema's own verb-dispatch shape — the installable positional is
// captured the SAME way just/npm/devbox capture a verb (no new capture
// machinery needed, see registry_breadth.go's own "---- nix run ----"
// doc comment) — plus the ONE genuinely new piece this sub-slice added,
// DefaultVerb: unlike every prior VerbFamily schema, a BARE `nix run`
// (zero positionals) still dispatches, to the schema-declared default "."
// (nix run's own documented/verified-live current-directory default),
// rather than falling back to a safe, effect-less listing.
func TestVerbDispatch_NixRun(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("nix")
	if !ok {
		t.Fatal("nix not registered")
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("no interpreter resolved for nix")
	}

	t.Run("bare nix run dispatches the implicit default installable, not a safe listing", func(t *testing.T) {
		got := in.Interpret(leaf(t, "nix run"), schema, Context{})
		want := []Effect{{Kind: EffectExec, Family: "nix", Operation: ".", Source: "implicit"}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("explicit installable captured verbatim as Operation", func(t *testing.T) {
		got := in.Interpret(leaf(t, "nix run .#build"), schema, Context{})
		want := []Effect{{Kind: EffectExec, Family: "nix", Operation: ".#build", Source: "arg 0"}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("a remote/registry installable is captured the same way — classification is a policy concern, not this layer's", func(t *testing.T) {
		got := in.Interpret(leaf(t, "nix run nixpkgs#hello"), schema, Context{})
		want := []Effect{{Kind: EffectExec, Family: "nix", Operation: "nixpkgs#hello", Source: "arg 0"}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("trailing args after -- are opaque", func(t *testing.T) {
		got := in.Interpret(leaf(t, "nix run .#build -- --verbose"), schema, Context{})
		want := []Effect{{Kind: EffectExec, Family: "nix", Operation: ".#build", Source: "arg 0"}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("any flag before the installable fails closed — nixRunSchema deliberately models zero flags", func(t *testing.T) {
		got := in.Interpret(leaf(t, "nix run --impure .#build"), schema, Context{})
		if got.Sufficient {
			t.Errorf("got Sufficient=true, want --impure (unmodeled) to fail the interpretation closed: %+v", got)
		}
	})

	t.Run("dynamic installable captured as a Dynamic EffectExec, still Sufficient", func(t *testing.T) {
		got := in.Interpret(leaf(t, `nix run "$INSTALLABLE"`), schema, Context{})
		want := []Effect{{Kind: EffectExec, Family: "nix", Dynamic: true, Source: "arg 0"}}
		if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
			t.Errorf("got %+v, want effects %+v", got, want)
		}
	})

	t.Run("an unmodeled nix subcommand stays unmodeled (not this slice's job)", func(t *testing.T) {
		got := in.Interpret(leaf(t, "nix build .#foo"), schema, Context{})
		if got.Sufficient {
			t.Errorf("got Sufficient=true for nix build, want unmodeled subcommand insufficiency: %+v", got)
		}
	})
}

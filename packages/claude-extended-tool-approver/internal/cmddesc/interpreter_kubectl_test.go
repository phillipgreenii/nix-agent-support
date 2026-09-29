package cmddesc

import (
	"reflect"
	"strings"
	"testing"
)

// TestKubectlIsRegistryOnlyDispatch mirrors TestGoIsRegistryOnlySubcommand:
// kubectlInterpreter reads only the schema and the leaf, never schema.Name —
// renaming the schema must not change the result.
func TestKubectlIsRegistryOnlyDispatch(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("kubectl")
	if !ok {
		t.Fatal("kubectl not registered")
	}
	if schema.Interpreter != "kubectl" {
		t.Fatalf("kubectl names interpreter %q, want \"kubectl\"", schema.Interpreter)
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("kubectl interpreter not registered")
	}
	l := leaf(t, "kubectl --context dev get pods")
	got := in.Interpret(l, schema, Context{})
	renamed := schema
	renamed.Name = "not-kubectl"
	if again := in.Interpret(l, renamed, Context{}); !reflect.DeepEqual(got, again) {
		t.Errorf("interpreter result depends on schema.Name:\n%+v\n%+v", got, again)
	}
	want := []Effect{
		{Kind: EffectRemote, Resource: "dev", Operation: "read", Family: "kubectl", Source: "implicit"},
		{Kind: EffectStdio, Stream: StreamStdout},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want effects %+v", got, want)
	}
}

// TestKubectlContextCapture: --context's value is threaded onto the
// EffectRemote regardless of which subcommand produced it; absent, it stays
// "" (Unknown to KubeContextPolicy); --server/--cluster naming the cluster
// some OTHER way does not make the context "known" (the ruling's own
// "treat --server/--cluster as making the context Unknown unless --context
// is also given").
func TestKubectlContextCapture(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("kubectl")
	in, _ := LookupInterpreter(schema.Interpreter)

	remoteEffect := func(effects []Effect) (Effect, bool) {
		for _, e := range effects {
			if e.Kind == EffectRemote {
				return e, true
			}
		}
		return Effect{}, false
	}

	t.Run("context given", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl --context dev get pods"), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "dev" || e.Dynamic {
			t.Errorf("got %+v", e)
		}
	})
	t.Run("no context at all", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl get pods"), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "" || e.Dynamic {
			t.Errorf("got %+v", e)
		}
	})
	t.Run("server and cluster given, no --context", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl --server https://x --cluster foo get pods"), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "" || e.Dynamic {
			t.Errorf("--server/--cluster without --context must leave the context Unknown, got %+v", e)
		}
	})
	t.Run("context is a runtime expansion", func(t *testing.T) {
		got := in.Interpret(leaf(t, `kubectl --context "$V" get pods`), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || !e.Dynamic {
			t.Errorf("got %+v, want Dynamic", e)
		}
	})
	t.Run("last --context wins", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl --context dev --context prod get pods"), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "prod" {
			t.Errorf("got %+v, want the LAST --context value", e)
		}
	})
}

// TestKubectlTargetFlagsAnywhereInArgv (P8, docket tc-o14i5.3, packet
// tc-o14i5.3.4): --context/--kubeconfig/--server are found wherever they
// appear in argv, not only before the subcommand — the gap
// kubectlScanTargetFlags fixes (see its own doc comment). A --context given
// AFTER the verb is now resolved exactly like one given before it; a
// --kubeconfig/--server given anywhere ALONGSIDE a --context forces Dynamic,
// since a caller-supplied kubeconfig/server can redefine what that context
// name means; a --kubeconfig/--server with NO --context at all leaves the
// pre-existing Resource=="" (Unknown) behavior untouched.
func TestKubectlTargetFlagsAnywhereInArgv(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("kubectl")
	in, _ := LookupInterpreter(schema.Interpreter)

	remoteEffect := func(effects []Effect) (Effect, bool) {
		for _, e := range effects {
			if e.Kind == EffectRemote {
				return e, true
			}
		}
		return Effect{}, false
	}

	t.Run("--context after the subcommand", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl get pods --context dev"), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "dev" || e.Dynamic {
			t.Errorf("got %+v", e)
		}
	})
	t.Run("--kubeconfig after the subcommand, no --context: unchanged Unknown", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl get pods --kubeconfig /tmp/other.yaml"), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "" || e.Dynamic {
			t.Errorf("got %+v, want Resource==\"\" and Dynamic==false, matching the pre-existing --server-without--context case", e)
		}
	})
	t.Run("--server after the subcommand, alongside --context: forces Dynamic", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl --context dev get pods --server https://other"), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "dev" || !e.Dynamic {
			t.Errorf("got %+v, want Resource==\"dev\" (text still visible) but Dynamic==true (untrustworthy under a different --server)", e)
		}
	})
	t.Run("--kubeconfig before --context: forces Dynamic regardless of order", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl --kubeconfig /tmp/other.yaml --context dev get pods"), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "dev" || !e.Dynamic {
			t.Errorf("got %+v, want Dynamic==true", e)
		}
	})
	t.Run("-s short form after the subcommand, alongside --context: forces Dynamic", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl --context dev get pods -s https://other"), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "dev" || !e.Dynamic {
			t.Errorf("got %+v, want Dynamic==true", e)
		}
	})
	t.Run("glued --context=value form, after the subcommand", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl get pods --context=dev"), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "dev" || e.Dynamic {
			t.Errorf("got %+v", e)
		}
	})
	t.Run("inline KUBECONFIG= env assignment forces Dynamic", func(t *testing.T) {
		got := in.Interpret(leaf(t, "KUBECONFIG=/tmp/other.yaml kubectl --context dev get pods"), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "dev" || !e.Dynamic {
			t.Errorf("got %+v, want Dynamic==true (KUBECONFIG env assignment is a kubeconfig override)", e)
		}
	})
	t.Run("ambient KUBECONFIG (process env) forces Dynamic", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl --context dev get pods"), schema, Context{Env: map[string]string{"KUBECONFIG": "/home/x/.kube/other"}})
		e, ok := remoteEffect(got.Effects)
		if !ok || e.Resource != "dev" || !e.Dynamic {
			t.Errorf("got %+v, want Dynamic==true (ambient KUBECONFIG is a kubeconfig override)", e)
		}
	})
}

// TestKubectlExecClassPersistentScope (P8, docket tc-o14i5.3, packet
// tc-o14i5.3.4, Binding decision "d": "exec into persistent containers/pods
// = remote-persistent scope"): exec/attach mark RemotePersistent true
// (running-container semantics); port-forward/debug/proxy do not.
func TestKubectlExecClassPersistentScope(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("kubectl")
	in, _ := LookupInterpreter(schema.Interpreter)

	remoteEffect := func(effects []Effect) (Effect, bool) {
		for _, e := range effects {
			if e.Kind == EffectRemote {
				return e, true
			}
		}
		return Effect{}, false
	}

	persistent := map[string]bool{
		"exec": true, "attach": true,
		"port-forward": false, "debug": false, "proxy": false,
	}
	for verb, want := range persistent {
		cmd := "kubectl --context dev " + verb + " pod"
		got := in.Interpret(leaf(t, cmd), schema, Context{})
		e, ok := remoteEffect(got.Effects)
		if !ok {
			t.Errorf("%s: no EffectRemote, got %+v", cmd, got.Effects)
			continue
		}
		if e.RemotePersistent != want {
			t.Errorf("%s: RemotePersistent = %v, want %v", cmd, e.RemotePersistent, want)
		}
	}
}

// TestKubectlKubeconfigIsPathRead: --kubeconfig FILE is a genuine PathRead
// effect, unlike a plain subcommand-dispatch schema's global flags, which
// never get resolve()'d at all (see kubectlInterpreter's own doc comment for
// why this schema's bespoke interpreter calls resolve() on the parent scan).
func TestKubectlKubeconfigIsPathRead(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("kubectl")
	in, _ := LookupInterpreter(schema.Interpreter)
	got := in.Interpret(leaf(t, "kubectl --kubeconfig /tmp/kc.yaml --context dev get pods"), schema, Context{})
	found := false
	for _, e := range got.Effects {
		if e.Kind == EffectPath && e.Path == "/tmp/kc.yaml" && e.Access == AccessRead {
			found = true
		}
	}
	if !found {
		t.Errorf("no PathRead effect for --kubeconfig's value, got %+v", got.Effects)
	}
}

// TestKubectlConfigNestedSubcommand mirrors TestGoModIsNestedSubcommand:
// "config" is itself a Subcommands dispatch table, recursed into through the
// ordinary generic interpretSubcommand path (kubectlInterpreter only wraps
// the TOP level) — a read sub-verb and a mutation sub-verb both get the
// context stamped identically.
func TestKubectlConfigNestedSubcommand(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("kubectl")
	in, _ := LookupInterpreter(schema.Interpreter)

	read := in.Interpret(leaf(t, "kubectl --context dev config get-contexts"), schema, Context{})
	if !read.Sufficient {
		t.Fatalf("config get-contexts: got insufficient: %s", read.Insufficiency)
	}
	wantRead := Effect{Kind: EffectRemote, Resource: "dev", Operation: "read", Family: "kubectl", Source: "implicit"}
	if !reflect.DeepEqual(read.Effects[0], wantRead) {
		t.Errorf("config get-contexts: got %+v, want %+v", read.Effects[0], wantRead)
	}

	mutate := in.Interpret(leaf(t, "kubectl --context dev config use-context foo"), schema, Context{})
	if !mutate.Sufficient {
		t.Fatalf("config use-context: got insufficient: %s", mutate.Insufficiency)
	}
	wantMutate := Effect{Kind: EffectRemote, Resource: "dev", Operation: "mutation", Family: "kubectl", Source: "implicit"}
	if !reflect.DeepEqual(mutate.Effects[0], wantMutate) {
		t.Errorf("config use-context: got %+v, want %+v", mutate.Effects[0], wantMutate)
	}
}

// TestKubectlExecClassUnconditionalInsufficiency mirrors go install/get's
// ImplicitEffect{Role: Unmodeled} precedent: every exec-class verb
// (exec/port-forward/attach/debug/proxy) is insufficient REGARDLESS of
// flags or positionals — including the bare, zero-argument form no golden
// exercises — while STILL emitting the Remote("exec") effect (so the class
// is visible in the graph and judged like everything else).
func TestKubectlExecClassUnconditionalInsufficiency(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("kubectl")
	in, _ := LookupInterpreter(schema.Interpreter)

	for _, cmd := range []string{
		"kubectl --context dev exec -it pod -- sh",
		"kubectl --context dev proxy",
		"kubectl --context dev port-forward pod 8080:80",
		"kubectl --context dev attach pod",
		"kubectl --context dev debug pod",
	} {
		got := in.Interpret(leaf(t, cmd), schema, Context{})
		if got.Sufficient {
			t.Errorf("%s: got sufficient, want unconditionally insufficient", cmd)
		}
		if !strings.Contains(got.Insufficiency, "unmodeled implicit effect role") {
			t.Errorf("%s: insufficiency = %q, want the unmodeled-implicit-effect fail-closed text", cmd, got.Insufficiency)
		}
		found := false
		for _, e := range got.Effects {
			if e.Kind == EffectRemote && e.Operation == "exec" && e.Family == "kubectl" && e.Resource == "dev" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no stamped Remote(\"exec\") effect, got %+v", cmd, got.Effects)
		}
	}
}

// TestKubectlDryRunFlagSpellings: --dry-run=client marks the mutation
// DryRun via the exact-flag-spelling mechanism (schema.Flags keyed by the
// WHOLE "--dry-run=value" text); --dry-run=server and --dry-run=none do
// not mark it — the effect stays an ordinary, unmarked "mutation".
func TestKubectlDryRunFlagSpellings(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("kubectl")
	in, _ := LookupInterpreter(schema.Interpreter)

	dryRunOf := func(cmd string) bool {
		got := in.Interpret(leaf(t, cmd), schema, Context{})
		for _, e := range got.Effects {
			if e.Kind == EffectRemote && e.Operation == "mutation" {
				return e.DryRun
			}
		}
		t.Fatalf("%s: no mutation EffectRemote found", cmd)
		return false
	}
	cases := map[string]bool{
		"kubectl --context dev apply --dry-run=client -f x.yaml": true,
		"kubectl --context dev apply --dry-run=server -f x.yaml": false,
		"kubectl --context dev apply --dry-run=none -f x.yaml":   false,
		"kubectl --context dev apply -f x.yaml":                  false,
	}
	for cmd, want := range cases {
		if got := dryRunOf(cmd); got != want {
			t.Errorf("%s: DryRun = %v, want %v", cmd, got, want)
		}
	}
}

// TestKubectlManifestStdinDash: `-f -` reads STANDARD INPUT (an EffectStdio
// stdin effect), never a path literally named "-" — the generic
// StdinToken convention (cat/head/wc/sort/tail's own "-") only ever fires
// for a POSITIONAL operand, never a flag's value, so kubectl's `-f -` needs
// its own special case (kubectlManifestInterpreter).
func TestKubectlManifestStdinDash(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("kubectl")
	in, _ := LookupInterpreter(schema.Interpreter)

	got := in.Interpret(leaf(t, "kubectl --context dev apply -f -"), schema, Context{})
	var sawStdin, sawDashPath bool
	for _, e := range got.Effects {
		if e.Kind == EffectStdio && e.Stream == StreamStdin {
			sawStdin = true
		}
		if e.Kind == EffectPath && e.Path == "-" {
			sawDashPath = true
		}
	}
	if !sawStdin {
		t.Errorf("`-f -` did not produce a stdin effect: %+v", got.Effects)
	}
	if sawDashPath {
		t.Errorf("`-f -` wrongly produced a path effect for a file literally named \"-\": %+v", got.Effects)
	}

	// An ordinary file operand must NOT be mistaken for stdin.
	ordinary := in.Interpret(leaf(t, "kubectl --context dev apply -f manifest.yaml"), schema, Context{})
	for _, e := range ordinary.Effects {
		if e.Kind == EffectStdio && e.Stream == StreamStdin {
			t.Errorf("`-f manifest.yaml` wrongly produced a stdin effect: %+v", ordinary.Effects)
		}
	}
}

// TestKubectlCpLocalRemoteSplit: kubectlCpInterpreter classifies each
// positional local/remote by its own text (kubectl's `[namespace/]pod:path`
// colon convention) — the LOCAL operand becomes a real PathRead (source) or
// PathTruncate (destination) effect; the REMOTE one is skipped entirely.
// The whole interpretation is unconditionally insufficient regardless
// (matching the exec-class family's "leaf is insufficient/Unknown"), but an
// insufficient interpretation still carries the effects it understood — the
// mechanism this slice's golden `kubectl --context dev cp pod:/etc/x
// ~/.ssh/id_rsa` case relies on to let a Forbidden local write win the fold
// even though cp is never sufficient.
func TestKubectlCpLocalRemoteSplit(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("kubectl")
	in, _ := LookupInterpreter(schema.Interpreter)

	t.Run("local source, remote destination", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl --context dev cp README.md pod:/etc/x"), schema, Context{})
		if got.Sufficient {
			t.Error("kubectl cp must always be insufficient")
		}
		want := Effect{Kind: EffectPath, Path: "README.md", Access: AccessRead, Source: "arg 0", FromPositional: true}
		if !reflect.DeepEqual(got.Effects[0], want) {
			t.Errorf("got %+v, want %+v", got.Effects[0], want)
		}
		for _, e := range got.Effects {
			if e.Kind == EffectPath && e.Path != "README.md" {
				t.Errorf("unexpected extra path effect for the remote operand: %+v", e)
			}
		}
	})
	t.Run("remote source, local destination", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl --context dev cp pod:/etc/x local/dest"), schema, Context{})
		if got.Sufficient {
			t.Error("kubectl cp must always be insufficient")
		}
		want := Effect{Kind: EffectPath, Path: "local/dest", Access: AccessTruncate, Source: "arg 1", FromPositional: true}
		if !reflect.DeepEqual(got.Effects[0], want) {
			t.Errorf("got %+v, want %+v", got.Effects[0], want)
		}
	})
	t.Run("exec-class Remote effect is stamped with the context", func(t *testing.T) {
		got := in.Interpret(leaf(t, "kubectl --context dev cp README.md pod:/etc/x"), schema, Context{})
		found := false
		for _, e := range got.Effects {
			if e.Kind == EffectRemote && e.Operation == "exec" && e.Family == "kubectl" && e.Resource == "dev" {
				found = true
			}
		}
		if !found {
			t.Errorf("no stamped Remote(\"exec\") effect, got %+v", got.Effects)
		}
	})
}

// TestKubectlUnmodeledSubcommandFallback mirrors go's own doc/tool/work and
// git's worktree frobnicate precedent: an unlisted verb needs no schema at
// all — interpretSubcommand's (here, kubectlInterpreter's own mirror of it)
// existing "unmodeled subcommand" fallback already abstains.
func TestKubectlUnmodeledSubcommandFallback(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("kubectl")
	in, _ := LookupInterpreter(schema.Interpreter)
	got := in.Interpret(leaf(t, "kubectl --context dev frobnicate"), schema, Context{})
	if got.Sufficient {
		t.Error("got sufficient, want insufficient (unmodeled subcommand)")
	}
	if !strings.Contains(got.Insufficiency, "unmodeled subcommand frobnicate") {
		t.Errorf("insufficiency = %q", got.Insufficiency)
	}
	if len(got.Effects) != 0 {
		t.Errorf("an unmodeled subcommand must produce no effects, got %+v", got.Effects)
	}
}

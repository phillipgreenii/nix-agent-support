package cmddesc

import (
	"reflect"
	"testing"
)

// TestScpIsRegistryOnlyDispatch mirrors TestSshIsRegistryOnlyDispatch:
// scpInterpreter reads only the schema and the leaf, never schema.Name.
func TestScpIsRegistryOnlyDispatch(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("scp")
	if !ok {
		t.Fatal("scp not registered")
	}
	if schema.Interpreter != "scp" {
		t.Fatalf("scp names interpreter %q, want \"scp\"", schema.Interpreter)
	}
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatal("scp interpreter not registered")
	}
	l := leaf(t, "scp README.md host:/tmp/")
	got := in.Interpret(l, schema, Context{})
	renamed := schema
	renamed.Name = "not-scp"
	if again := in.Interpret(l, renamed, Context{}); !reflect.DeepEqual(got, again) {
		t.Errorf("interpreter result depends on schema.Name:\n%+v\n%+v", got, again)
	}
}

func pathEffects(effects []Effect) []Effect {
	var out []Effect
	for _, e := range effects {
		if e.Kind == EffectPath {
			out = append(out, e)
		}
	}
	return out
}

func netEffects(effects []Effect) []Effect {
	var out []Effect
	for _, e := range effects {
		if e.Kind == EffectNet {
			out = append(out, e)
		}
	}
	return out
}

// TestScpOperandClassification covers the local/remote split the brief
// names: plain "[user@]host:path", a "scp://" URI, a local path that merely
// CONTAINS a colon after a slash (stays local), and a local path with no
// slash at all whose colon still makes it remote (matching production's own
// isRemoteToken — a bare "file:name" is remote by the same text-only rule
// real scp applies).
func TestScpOperandClassification(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("scp")
	in, _ := LookupInterpreter(schema.Interpreter)

	cases := []struct {
		name       string
		command    string
		wantRemote bool
		wantHost   string
		wantPath   string
	}{
		{"plain-host-path", "scp README.md host:/tmp/x", true, "host", "/tmp/x"},
		{"user-at-host", "scp README.md user@host:/tmp/x", true, "host", "/tmp/x"},
		{"scp-url", "scp README.md scp://user@host:2222/tmp/x", true, "host", "/tmp/x"},
		{"local-colon-after-slash", "scp ./file:name host:/tmp/", false, "", "./file:name"},
		{"local-plain", "scp README.md /tmp/dest", false, "", "/tmp/dest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := in.Interpret(leaf(t, tc.command), schema, Context{})
			var found bool
			for _, e := range pathEffects(res.Effects) {
				if tc.wantRemote && e.Remote != "" {
					found = true
					if e.Remote != tc.wantHost {
						t.Errorf("host = %q, want %q", e.Remote, tc.wantHost)
					}
					if e.Path != tc.wantPath {
						t.Errorf("path = %q, want %q", e.Path, tc.wantPath)
					}
				}
				if !tc.wantRemote && e.Path == tc.wantPath {
					found = true
					if e.Remote != "" {
						t.Errorf("unexpectedly remote: %+v", e)
					}
				}
			}
			if !found {
				t.Fatalf("no matching path effect in %+v", res.Effects)
			}
		})
	}
}

// TestScpDestinationIsLastPositional: the LAST positional is always the
// destination (AccessTruncate); every earlier one is a source (AccessRead) —
// including with more than two positionals (multiple local sources into one
// remote destination).
func TestScpDestinationIsLastPositional(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("scp")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "scp a.txt b.txt host:/tmp/"), schema, Context{})

	var reads, truncates int
	for _, e := range pathEffects(res.Effects) {
		switch e.Access {
		case AccessRead:
			reads++
			if e.Path != "a.txt" && e.Path != "b.txt" {
				t.Errorf("unexpected read path %q", e.Path)
			}
		case AccessTruncate:
			truncates++
			if e.Path != "/tmp/" || e.Remote != "host" {
				t.Errorf("unexpected destination effect %+v", e)
			}
		default:
			t.Errorf("unexpected access class on %+v", e)
		}
	}
	if reads != 2 || truncates != 1 {
		t.Errorf("reads=%d truncates=%d, want 2 and 1 (effects: %+v)", reads, truncates, res.Effects)
	}
}

// TestScpRemoteToRemote (`-3`, the default mode, transferring through the
// local host) still classifies BOTH operands remote, and emits ONE
// EffectNet per DISTINCT host.
func TestScpRemoteToRemote(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("scp")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "scp -3 a:/x b:/y"), schema, Context{})

	paths := pathEffects(res.Effects)
	if len(paths) != 2 {
		t.Fatalf("path effects = %+v, want 2", paths)
	}
	for _, e := range paths {
		if e.Remote == "" {
			t.Errorf("expected remote path effect, got %+v", e)
		}
	}
	nets := netEffects(res.Effects)
	if len(nets) != 2 {
		t.Fatalf("net effects = %+v, want 2 (one per distinct host)", nets)
	}
	hosts := map[string]bool{}
	for _, e := range nets {
		hosts[e.Host] = true
		if e.Direction != NetOutbound {
			t.Errorf("direction = %v, want NetOutbound", e.Direction)
		}
	}
	if !hosts["a"] || !hosts["b"] {
		t.Errorf("hosts = %+v, want {a, b}", hosts)
	}
}

// TestScpDuplicateHostSingleNetEffect: the SAME host referenced by more than
// one operand gets exactly ONE EffectNet, not one per operand.
func TestScpDuplicateHostSingleNetEffect(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("scp")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "scp host:/a host:/b"), schema, Context{})
	nets := netEffects(res.Effects)
	if len(nets) != 1 {
		t.Fatalf("net effects = %+v, want exactly 1", nets)
	}
	if nets[0].Host != "host" {
		t.Errorf("host = %q, want %q", nets[0].Host, "host")
	}
}

// TestScpEmptyRemotePathIsHomeDir: a bare "host:" (no path at all) means the
// remote home directory.
func TestScpEmptyRemotePathIsHomeDir(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("scp")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "scp README.md host:"), schema, Context{})
	var found bool
	for _, e := range pathEffects(res.Effects) {
		if e.Remote == "host" {
			found = true
			if e.Path != "~" {
				t.Errorf("path = %q, want %q (home dir)", e.Path, "~")
			}
		}
	}
	if !found {
		t.Fatalf("no remote path effect in %+v", res.Effects)
	}
}

// TestScpDynamicOperandIsNeverAssumedRemote: a fully dynamic operand cannot
// be classified at all, so it is emitted as an ordinary Dynamic LOCAL-shaped
// path effect, never a remote one and never an EffectNet.
func TestScpDynamicOperandIsNeverAssumedRemote(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("scp")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, `scp "$F" host:/tmp/`), schema, Context{})

	var sawDynamic bool
	for _, e := range pathEffects(res.Effects) {
		if e.Path == "$F" {
			sawDynamic = true
			if !e.Dynamic {
				t.Errorf("expected Dynamic, got %+v", e)
			}
			if e.Remote != "" {
				t.Errorf("a dynamic operand must never be assumed remote: %+v", e)
			}
		}
	}
	if !sawDynamic {
		t.Fatalf("no path effect for $F in %+v", res.Effects)
	}
	nets := netEffects(res.Effects)
	if len(nets) != 1 || nets[0].Host != "host" {
		t.Errorf("nets = %+v, want exactly one EffectNet for host (not $F)", nets)
	}
}

// TestScpKeyMaterialIsNotAPathRead: -i FILE is a KEY REFERENCE
// (EffectKeyMaterial), never an EffectPath — the same rationale as ssh's
// own -i (cmddesc.KindKeyMaterial's doc comment).
func TestScpKeyMaterialIsNotAPathRead(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("scp")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "scp -i ~/.ssh/id_rsa README.md host:/tmp/"), schema, Context{})
	var sawKeyMaterial bool
	for _, e := range res.Effects {
		if e.Kind == EffectPath && e.Path == "~/.ssh/id_rsa" {
			t.Errorf("unexpected EffectPath for -i's value: %+v", e)
		}
		if e.Kind == EffectKeyMaterial {
			sawKeyMaterial = true
			if e.Path != "~/.ssh/id_rsa" {
				t.Errorf("key material path = %q, want %q", e.Path, "~/.ssh/id_rsa")
			}
		}
	}
	if !sawKeyMaterial {
		t.Errorf("expected an EffectKeyMaterial for -i, got %+v", res.Effects)
	}
}

// TestScpTooFewPositionalsIsInsufficient: scp needs at least a source and a
// destination.
func TestScpTooFewPositionalsIsInsufficient(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("scp")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "scp host:/etc/passwd"), schema, Context{})
	if res.Sufficient {
		t.Errorf("expected insufficient (only one positional)")
	}
}

// TestScpUnknownFlagIsInsufficient: an unmodeled scp flag fails closed like
// any other schema (UnknownFlagInsufficient, the default).
func TestScpUnknownFlagIsInsufficient(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("scp")
	in, _ := LookupInterpreter(schema.Interpreter)
	res := in.Interpret(leaf(t, "scp --not-a-real-flag README.md host:/tmp/"), schema, Context{})
	if res.Sufficient {
		t.Errorf("expected insufficient for an unmodeled flag")
	}
}

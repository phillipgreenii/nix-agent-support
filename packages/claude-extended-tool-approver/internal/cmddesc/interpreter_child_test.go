package cmddesc

import (
	"reflect"
	"strings"
	"testing"
)

// TestTeeIsRegistryOnly: the same proof as head/rm for tee — a plain schema
// value, the generic interpreter, -a applied as a generic transform.
func TestTeeIsRegistryOnly(t *testing.T) {
	reg := DefaultRegistry()
	schema, ok := reg.Lookup("tee")
	if !ok || schema.Interpreter != "" {
		t.Fatalf("tee schema = %+v, ok=%v; must be a generic registry entry", schema, ok)
	}
	in, _ := LookupInterpreter("")
	l := leaf(t, "tee -a out.txt")
	got := in.Interpret(l, schema, Context{})
	renamed := schema
	renamed.Name = "not-tee"
	if again := in.Interpret(l, renamed, Context{}); !reflect.DeepEqual(got, again) {
		t.Errorf("interpreter result depends on schema.Name")
	}
	want := []Effect{
		{Kind: EffectPath, Path: "out.txt", Access: AccessModify, Source: "arg 1", FromPositional: true},
		{Kind: EffectStdio, Stream: StreamStdin, Source: "stdin"},
		{Kind: EffectStdio, Stream: StreamStdout},
	}
	if !got.Sufficient || !reflect.DeepEqual(got.Effects, want) {
		t.Errorf("got %+v, want effects %+v", got, want)
	}
	plain := in.Interpret(leaf(t, "tee out.txt"), schema, Context{})
	if plain.Effects[0].Access != AccessTruncate {
		t.Errorf("tee without -a: access = %s, want truncate", plain.Effects[0].Access)
	}
}

// TestShIsBashRenamed: sh is bash's schema value under a second key.
func TestShIsBashRenamed(t *testing.T) {
	reg := DefaultRegistry()
	bash, _ := reg.Lookup("bash")
	sh, _ := reg.Lookup("sh")
	sh.Name = bash.Name
	if !reflect.DeepEqual(bash, sh) {
		t.Errorf("sh differs from bash beyond Name")
	}
}

// TestShellDialectHandsBackChild: `-c PROGRAM` becomes one shell child
// labelled by the flag that carried it; a script-file positional has no
// dialect interpreter and is insufficient; a dynamic program is insufficient.
func TestShellDialectHandsBackChild(t *testing.T) {
	reg := DefaultRegistry()
	bash, _ := reg.Lookup("bash")
	in := GenericInterpreter{}

	got := in.Interpret(leaf(t, "bash -xc 'cat a' arg0"), bash, Context{})
	if !got.Sufficient {
		t.Fatalf("insufficient: %s", got.Insufficiency)
	}
	wantChildren := []ChildInvocation{{Dialect: "shell", Program: "cat a", Source: "bash -c"}}
	if !reflect.DeepEqual(got.Children, wantChildren) {
		t.Errorf("children = %+v, want %+v", got.Children, wantChildren)
	}

	file := in.Interpret(leaf(t, "bash script.sh -x"), bash, Context{})
	if file.Sufficient || !strings.Contains(file.Insufficiency, "no interpreter for dialect shell-file") {
		t.Errorf("script file: %+v", file)
	}
	if len(file.Effects) != 1 || file.Effects[0].Dialect != "shell-file" {
		t.Errorf("script file effects = %+v", file.Effects)
	}

	dyn := in.Interpret(leaf(t, `sh -c "cat $F"`), bash, Context{})
	if dyn.Sufficient || len(dyn.Children) != 0 || !strings.Contains(dyn.Insufficiency, "runtime expansion") {
		t.Errorf("dynamic program: %+v", dyn)
	}

	noexec := in.Interpret(leaf(t, "bash -n -c 'cat a'"), bash, Context{})
	if noexec.Sufficient {
		t.Errorf("-n must be an unknown flag: %+v", noexec)
	}
}

func TestXargsInterpreter(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("xargs")
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatalf("no interpreter %q", schema.Interpreter)
	}
	cases := []struct {
		name       string
		command    string
		sufficient bool
		argv       []string
		dynamic    []bool
		reads      []string
		reason     string
	}{
		{"appended item", "xargs rm -f", true, []string{"rm", "-f", "<stdin-item>"}, []bool{false, false, true}, nil, ""},
		{"replacement token", "xargs -I{} cp {} sub", true, []string{"cp", "{}", "sub"}, []bool{false, true, false}, nil, ""},
		{"replacement token embedded", "xargs -I R cp R.bak sub", true, []string{"cp", "R.bak", "sub"}, []bool{false, true, false}, nil, ""},
		{"-i default token", "xargs -i mv {} sub", true, []string{"mv", "{}", "sub"}, []bool{false, true, false}, nil, ""},
		{"--replace=R", "xargs --replace=X cat X", true, []string{"cat", "X"}, []bool{false, true}, nil, ""},
		{"live expansion is dynamic", `xargs rm "$F"`, true, []string{"rm", "$F", "<stdin-item>"}, []bool{false, true, true}, nil, ""},
		{"-a file is read", "xargs -a list.txt rm", true, []string{"rm", "<stdin-item>"}, []bool{false, true}, []string{"list.txt"}, ""},
		{"child flags are not xargs flags", "xargs -n1 rm -rf --x", true, []string{"rm", "-rf", "--x", "<stdin-item>"}, []bool{false, false, false, true}, nil, ""},
		{"no command", "xargs -n1", false, nil, nil, nil, "no command operand"},
		{"interactive left out", "xargs -p rm", false, nil, nil, nil, "unknown flag -p"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := in.Interpret(leaf(t, tc.command), schema, Context{})
			if got.Sufficient != tc.sufficient {
				t.Fatalf("sufficient = %v (%s), want %v", got.Sufficient, got.Insufficiency, tc.sufficient)
			}
			if !tc.sufficient {
				if !strings.Contains(got.Insufficiency, tc.reason) {
					t.Errorf("insufficiency = %q, want substring %q", got.Insufficiency, tc.reason)
				}
				return
			}
			if len(got.Children) != 1 {
				t.Fatalf("children = %+v, want one argv child", got.Children)
			}
			c := got.Children[0]
			if c.Dialect != "argv" || c.Source != "xargs" || !reflect.DeepEqual(c.Argv, tc.argv) || !reflect.DeepEqual(c.ArgvDynamic, tc.dynamic) {
				t.Errorf("child = %+v, want argv %q dynamic %v", c, tc.argv, tc.dynamic)
			}
			var reads []string
			for _, e := range got.Effects {
				if e.Kind == EffectPath && e.Access == AccessRead {
					reads = append(reads, e.Path)
				}
			}
			if !reflect.DeepEqual(reads, tc.reads) {
				t.Errorf("reads = %v, want %v", reads, tc.reads)
			}
		})
	}
}

func TestURLHost(t *testing.T) {
	cases := map[string]string{
		"https://example.com/x":              "example.com",
		"http://Example.COM":                 "example.com",
		"example.com/x":                      "example.com",
		"example.com":                        "example.com",
		"https://example.com:8443/x":         "example.com",
		"localhost:8080/x":                   "localhost",
		"https://user:pw@example.com/x":      "example.com",
		"http://[::1]:8080/x":                "::1",
		"https://api.example.com?q=1#f":      "api.example.com",
		"ftp://files.example.com/pub":        "files.example.com",
		"file:///etc/passwd":                 "",
		"/just/a/path":                       "",
		"https://":                           "",
		"http://[::1":                        "",
		"https://example.com/@notuserinfo/x": "example.com",
		"https://x@example.com:443/@y":       "example.com",
	}
	for raw, want := range cases {
		got, ok := urlHost(raw)
		if got != want || ok != (want != "") {
			t.Errorf("urlHost(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
}

func TestCurlInterpreter(t *testing.T) {
	reg := DefaultRegistry()
	schema, _ := reg.Lookup("curl")
	in, ok := LookupInterpreter(schema.Interpreter)
	if !ok {
		t.Fatalf("no interpreter %q", schema.Interpreter)
	}
	net := func(host, method string, dir NetDirection, src string) Effect {
		return Effect{Kind: EffectNet, Host: host, Direction: dir, Method: method, Source: src}
	}
	cases := []struct {
		name       string
		command    string
		sufficient bool
		nets       []Effect
		reason     string
	}{
		{"plain get", "curl https://example.com/x", true, []Effect{net("example.com", "GET", NetInbound, "arg 0")}, ""},
		{"no scheme with port", "curl localhost:8080/health", true, []Effect{net("localhost", "GET", NetInbound, "arg 0")}, ""},
		{"head", "curl -I https://example.com", true, []Effect{net("example.com", "HEAD", NetInbound, "arg 1")}, ""},
		{"head overrides -X", "curl -X POST -I https://example.com", true, []Effect{net("example.com", "HEAD", NetInbound, "arg 3")}, ""},
		{"-X get is inbound", "curl -X GET https://example.com", true, []Effect{net("example.com", "GET", NetInbound, "arg 2")}, ""},
		{"-X delete is outbound", "curl -X DELETE https://example.com", true, []Effect{net("example.com", "DELETE", NetOutbound, "arg 2")}, ""},
		{"-X lowercase normalised", "curl --request=options https://example.com", true, []Effect{net("example.com", "OPTIONS", NetInbound, "arg 1")}, ""},
		{"data implies post", "curl -d x=1 https://example.com", true, []Effect{net("example.com", "POST", NetOutbound, "arg 2")}, ""},
		{"form implies post", "curl -F f=@a.txt https://example.com", true, []Effect{net("example.com", "POST", NetOutbound, "arg 2")}, ""},
		{"upload implies put", "curl -T a.txt https://example.com", true, []Effect{net("example.com", "PUT", NetOutbound, "arg 2")}, ""},
		{"-G keeps get but data leaves", "curl -G -d q=1 https://example.com", true, []Effect{net("example.com", "GET", NetOutbound, "arg 3")}, ""},
		{"--url and two urls", "curl --url https://a.example https://b.example", true, []Effect{net("b.example", "GET", NetInbound, "arg 2"), net("a.example", "GET", NetInbound, "arg 1")}, ""},
		{"dynamic url", `curl "$URL"`, true, []Effect{{Kind: EffectNet, Host: "$URL", Direction: NetInbound, Method: "GET", Dynamic: true, Source: "arg 0"}}, ""},
		{"no url", "curl -s", false, nil, "no URL operand"},
		{"unparseable host", "curl file:///etc/passwd", false, nil, "cannot determine the host"},
		{"dynamic method", `curl -X "$M" https://example.com`, false, nil, "runtime expansion"},
		{"insecure left out", "curl -k https://example.com", false, nil, "unknown flag -k"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := in.Interpret(leaf(t, tc.command), schema, Context{})
			if got.Sufficient != tc.sufficient {
				t.Fatalf("sufficient = %v (%s), want %v", got.Sufficient, got.Insufficiency, tc.sufficient)
			}
			if !tc.sufficient {
				if !strings.Contains(got.Insufficiency, tc.reason) {
					t.Errorf("insufficiency = %q, want substring %q", got.Insufficiency, tc.reason)
				}
				return
			}
			var nets []Effect
			for _, e := range got.Effects {
				if e.Kind == EffectNet {
					nets = append(nets, e)
				}
			}
			if !reflect.DeepEqual(nets, tc.nets) {
				t.Errorf("net effects = %+v\nwant        %+v", nets, tc.nets)
			}
		})
	}
}

// TestDataOrAtFile: the `@` convention is applied by the generic interpreter
// from the role alone.
func TestDataOrAtFile(t *testing.T) {
	s := CommandSchema{
		Name:        "x",
		Flags:       map[string]FlagSpec{"-d": {Arity: ArityOne, Operand: DataOrAtFile}},
		Positionals: PositionalSpec{Rest: Literal},
	}
	cases := []struct {
		name    string
		command string
		effects []Effect
	}{
		{"plain data is inert", "x -d 'a=b'", nil},
		{"@- consumes stdin", "x -d @-", []Effect{{Kind: EffectStdio, Stream: StreamStdin, Source: "stdin"}}},
		{"@path reads path", "x -d @in.json", []Effect{{Kind: EffectPath, Path: "in.json", Access: AccessRead, Source: "arg 1"}}},
		{"@ live path is dynamic", `x -d "@$F"`, []Effect{{Kind: EffectPath, Path: "$F", Access: AccessRead, Dynamic: true, Source: "arg 1"}}},
		{"live value may be @file", `x -d "$DATA"`, []Effect{{Kind: EffectPath, Path: "$DATA", Access: AccessRead, Dynamic: true, Source: "arg 1", Detail: "may expand to @file"}}},
		{"positional literal unaffected", "x @not-data", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GenericInterpreter{}.Interpret(leaf(t, tc.command), s, Context{})
			if !got.Sufficient {
				t.Fatalf("insufficient: %s", got.Insufficiency)
			}
			if !reflect.DeepEqual(got.Effects, tc.effects) {
				t.Errorf("effects = %+v\nwant     %+v", got.Effects, tc.effects)
			}
		})
	}
}

func TestNetEffectString(t *testing.T) {
	cases := map[Effect]string{
		{Kind: EffectNet, Host: "h", Direction: NetOutbound, Method: "POST", Source: "arg 2"}:            "net:outbound h POST [arg 2]",
		{Kind: EffectNet, Host: "$U", Direction: NetInbound, Method: "GET", Dynamic: true}:               "net:inbound $U GET (dynamic)",
		{Kind: EffectPath, Path: "$D", Access: AccessRead, Dynamic: true, Detail: "may expand to @file"}: "path:read $D (dynamic) (may expand to @file)",
	}
	for e, want := range cases {
		if got := e.String(); got != want {
			t.Errorf("%+v.String() = %q, want %q", e, got, want)
		}
	}
}

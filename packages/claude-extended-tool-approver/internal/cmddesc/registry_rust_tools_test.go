package cmddesc

import (
	"reflect"
	"testing"
)

// TestSdAndAstGrepEffects pins the effect shapes of the sd and ast-grep
// schemas (pg2-ve5xen): writes are path-modify effects under the same path
// policy as sed -i, previews are reads, and every unmodeled shape is
// insufficient (abstain).
func TestSdAndAstGrepEffects(t *testing.T) {
	type want struct {
		sufficient bool
		reads      []string
		modifies   []string
		dynamic    bool // some path effect is Dynamic (judged later, never approved)
	}
	cases := []struct {
		name string
		cmd  string
		want want
	}{
		{"sd stdin", `sd a b`, want{true, nil, nil, false}},
		{"sd fixed strings stdin", `sd -F 'a.b' 'c'`, want{true, nil, nil, false}},
		{"sd file is modify", `sd a b file.txt`, want{true, nil, []string{"file.txt"}, false}},
		{"sd two files", `sd -n 1 -f i a b x y`, want{true, nil, []string{"x", "y"}, false}},
		{"sd preview is read", `sd -p a b file.txt`, want{true, []string{"file.txt"}, nil, false}},
		{"sd --preview keeps secret path read", `sd --preview a b /home/u/.ssh/id_rsa`, want{true, []string{"/home/u/.ssh/id_rsa"}, nil, false}},
		{"sd secret path write stays modify", `sd a b /home/u/.ssh/id_rsa`, want{true, nil, []string{"/home/u/.ssh/id_rsa"}, false}},
		{"sd live-expansion FILE abstains", `sd a b "$F"`, want{sufficient: true, dynamic: true}},
		{"sd unknown flag abstains", `sd --bogus a b f`, want{sufficient: false}},
		{"sd missing replacement abstains", `sd a`, want{sufficient: false}},
		{"ast-grep search", `ast-grep run -p 'foo($A)' -l go src`, want{true, []string{"src"}, nil, false}},
		{"ast-grep bare search", `ast-grep -p 'foo($A)'`, want{true, []string{"."}, nil, false}},
		{"ast-grep bare search with path", `ast-grep -p x src/a.go`, want{true, []string{"src/a.go"}, nil, false}},
		{"ast-grep rewrite without -U is read", `ast-grep run -p x -r y src`, want{true, []string{"src"}, nil, false}},
		{"ast-grep -U without path modifies .", `ast-grep run -p x -r y -U`, want{true, []string{"."}, []string{"."}, false}},
		{"ast-grep -U explicit path", `ast-grep run -p x -r y -U src`, want{true, nil, []string{"src"}, false}},
		{"ast-grep bare -U without path", `ast-grep -p x -r y --update-all`, want{true, []string{"."}, []string{"."}, false}},
		{"ast-grep scan", `ast-grep scan -r rule.yml src`, want{true, []string{"rule.yml", "src"}, nil, false}},
		{"ast-grep scan -U without path", `ast-grep scan -U`, want{true, []string{"."}, []string{"."}, false}},
		{"ast-grep test", `ast-grep test`, want{true, []string{"."}, nil, false}},
		{"ast-grep -i abstains", `ast-grep run -p x -r y -i`, want{sufficient: false}},
		{"ast-grep scan -i abstains", `ast-grep scan -i`, want{sufficient: false}},
		{"ast-grep test -U abstains", `ast-grep test -U`, want{sufficient: false}},
		{"ast-grep --stdin abstains", `ast-grep run -p x --stdin`, want{sufficient: false}},
		{"ast-grep new abstains", `ast-grep new project`, want{sufficient: false}},
		{"ast-grep lsp abstains", `ast-grep lsp`, want{sufficient: false}},
		{"ast-grep completions abstains", `ast-grep completions zsh`, want{sufficient: false}},
		{"ast-grep unknown flag abstains", `ast-grep run -p x --bogus`, want{sufficient: false}},
		{"ast-grep live-expansion path abstains", `ast-grep run -p x -U "$D"`, want{sufficient: true, dynamic: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := interpretDefault(t, tc.cmd)
			if got.Sufficient != tc.want.sufficient {
				t.Fatalf("Sufficient = %v (%s), want %v", got.Sufficient, got.Insufficiency, tc.want.sufficient)
			}
			if !tc.want.sufficient {
				return
			}
			if tc.want.dynamic {
				var dyn bool
				for _, e := range got.Effects {
					dyn = dyn || (e.Kind == EffectPath && e.Dynamic)
				}
				if !dyn {
					t.Fatalf("no Dynamic path effect in %+v", got.Effects)
				}
				return
			}
			if r := effectPaths(got.Effects, AccessRead); !reflect.DeepEqual(r, tc.want.reads) {
				t.Errorf("reads = %v, want %v", r, tc.want.reads)
			}
			if m := effectPaths(got.Effects, AccessModify); !reflect.DeepEqual(m, tc.want.modifies) {
				t.Errorf("modifies = %v, want %v", m, tc.want.modifies)
			}
		})
	}
}

// TestSgIsNotRegistered: only `ast-grep` is classified; `sg` is shadow-utils'
// newgrp on Linux, so it stays unregistered and prompts.
func TestSgIsNotRegistered(t *testing.T) {
	if _, ok := DefaultRegistry().Lookup("sg"); ok {
		t.Error("sg must not be registered")
	}
}

// TestAstGrepParentSharesRunFlags: the default-subcommand dispatch only hands
// the whole argv to run when the parent and run flag tables are identical.
func TestAstGrepParentSharesRunFlags(t *testing.T) {
	if !reflect.DeepEqual(astGrepSchema.Flags, astGrepRunSchema.Flags) {
		t.Error("ast-grep parent flags must equal run flags")
	}
}

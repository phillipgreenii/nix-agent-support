package redact

import (
	"reflect"
	"strings"
	"testing"
)

const root = "/home/synthetic-user/proj"

func TestPlantedSecretsRemoved(t *testing.T) {
	cases := []struct {
		class, in string
	}{
		{ClassSK, "key sk-" + strings.Repeat("a1", 12) + " end"},
		{ClassGHP, "tok ghp_" + strings.Repeat("Ab3", 12) + " end"},
		{ClassCloudKey, "id AKIA" + strings.Repeat("Q7", 8) + " end"},
		{ClassBearer, "Authorization: Bearer " + strings.Repeat("xY9.", 6) + " end"},
		{ClassEnvLine, "before\nSECRET_TOKEN=hunter2-fake\nafter"},
		{ClassHomePath, "see /home/other-user/notes.txt now"},
	}
	for _, c := range cases {
		out, counts := Text(c.in, root)
		if !strings.Contains(out, "[REDACTED:"+c.class+"]") {
			t.Errorf("%s: no marker in %q", c.class, out)
		}
		if counts[c.class] != 1 {
			t.Errorf("%s: count = %v", c.class, counts)
		}
		for _, leak := range []string{"a1a1a1", "Ab3Ab3", "Q7Q7", "xY9.", "hunter2", "other-user"} {
			if strings.Contains(out, leak) {
				t.Errorf("%s: leaked %q in %q", c.class, leak, out)
			}
		}
		// Same via JSON, nested.
		v, jc := Value(map[string]any{"k": []any{map[string]any{"s": c.in}, 3.0, true, nil}}, root)
		if jc[c.class] != 1 {
			t.Errorf("%s json counts = %v", c.class, jc)
		}
		if strings.Contains(v.(map[string]any)["k"].([]any)[0].(map[string]any)["s"].(string), "hunter2") {
			t.Errorf("%s json leaked", c.class)
		}
	}
}

func TestHomePathRelativeToScopeRoot(t *testing.T) {
	out, counts := Text("edit "+root+"/internal/x.go and "+root+" and /home/synthetic-user/other/y", root)
	want := "edit internal/x.go and . and [REDACTED:home-path]"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
	if counts[ClassHomePath] != 3 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestSiblingPrefixNotUnderRoot(t *testing.T) {
	out, _ := Text(root+"-evil/x", root)
	if out != "[REDACTED:home-path]" {
		t.Fatalf("got %q", out)
	}
}

func TestBenignUnchanged(t *testing.T) {
	in := "The task-" + strings.Repeat("z", 30) + " ran at 10:00; Bearer is a word; path src/a/b.go; lower=case\nsk-short"
	out, counts := Text(in, root)
	if out != in || counts.Total() != 0 {
		t.Fatalf("changed: %q %v", out, counts)
	}
}

func TestIdempotentAndDeterministic(t *testing.T) {
	in := "SECRET=x\nsk-" + strings.Repeat("b", 25) + " " + root + "/a /home/zed/q Bearer " + strings.Repeat("c", 20)
	once, _ := Text(in, root)
	twice, c2 := Text(once, root)
	if once != twice || c2.Total() != 0 {
		t.Fatalf("not idempotent: %q -> %q (%v)", once, twice, c2)
	}
	again, _ := Text(in, root)
	if again != once {
		t.Fatal("not deterministic")
	}
}

func TestValueKeysUntouchedAndInputNotMutated(t *testing.T) {
	in := map[string]any{"sk-" + strings.Repeat("d", 25): "SECRET=1", "n": 1.0}
	orig := map[string]any{"sk-" + strings.Repeat("d", 25): "SECRET=1", "n": 1.0}
	out, _ := Value(in, "")
	if !reflect.DeepEqual(in, orig) {
		t.Fatal("input mutated")
	}
	m := out.(map[string]any)
	if m["sk-"+strings.Repeat("d", 25)] != "[REDACTED:env-line]" || m["n"] != 1.0 {
		t.Fatalf("got %v", m)
	}
}

package jsonstrict_test

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/jsonstrict"
)

// u spells r as a JSON unicode escape (a backslash, u and four hex digits).
func u(r rune) string { return fmt.Sprintf("\\u%04x", r) }

func TestCheckNoDuplicateKeys(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		dup  bool   // whether a key is repeated
		key  string // the repeated key
		at   string // the pointer of the object that holds it
	}{
		{"the empty object", `{}`, false, "", ""},
		{"unique keys", `{"a":1,"b":{"c":2},"d":[1,2]}`, false, "", ""},
		{"scalars", `"x"`, false, "", ""},
		{"a scalar array", `[1,2,3,"a","a"]`, false, "", ""},
		{"the same key in sibling objects", `{"a":{"x":1},"b":{"x":2}}`, false, "", ""},
		{"the same key at two levels", `{"x":{"x":{"x":1}}}`, false, "", ""},
		{"a repeated key at the root", `{"type":"a","type":"b"}`, true, "type", ""},
		{"a repeated key with equal values", `{"v":1,"v":1}`, true, "v", ""},
		{"a repeated key after other keys", `{"a":1,"b":2,"a":3}`, true, "a", ""},
		{"a repeated key in a nested object", `{"data":{"fields":{"k":1,"k":2}}}`, true, "k", "/data/fields"},
		{"a repeated key in an object of an array", `{"kv":[{"k":1},{"k":1,"k":2}]}`, true, "k", "/kv/1"},
		{"an array of arrays of objects", `[[{"a":1}],[{"b":1,"b":2}]]`, true, "b", "/1/0"},
		{"the empty key", `{"":1,"":2}`, true, "", ""},
		{"the pointer is escaped", `{"a/b":{"~":{"x":1,"x":2}}}`, true, "x", "/a~1b/~0"},
		{"keys are compared after unescaping", `{"a":1,"` + u('a') + `":2}`, true, "a", ""},
		{"an escaped slash is the same key as a slash", `{"a\/b":1,"a/b":2}`, true, "a/b", ""},
		{"escape forms of one key", `{"` + u('\n') + `":1,"\n":2}`, true, "\n", ""},
		{"an escaped quote", `{"a\"b":1,"a` + u('"') + `b":2}`, true, `a"b`, ""},
		{"a repeated upper-case key", `{"Ab":1,"Ab":2}`, true, "Ab", ""},
		{"keys that differ in case are different", `{"a":1,"A":2}`, false, "", ""},
		{"a duplicate after a closed sibling", `{"a":{},"b":[],"a":1}`, true, "a", ""},
		{"a key repeated only in a sibling's value is fine", `{"a":{"a":1},"b":1}`, false, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := jsonstrict.CheckNoDuplicateKeys([]byte(tc.doc))
			if !tc.dup {
				if err != nil {
					t.Fatalf("CheckNoDuplicateKeys(%s) = %v, want nil", tc.doc, err)
				}
				return
			}
			var dup *jsonstrict.DuplicateKeyError
			if !errors.As(err, &dup) {
				t.Fatalf("CheckNoDuplicateKeys(%s) = %v, want a *DuplicateKeyError", tc.doc, err)
			}
			if dup.Key != tc.key || dup.Pointer != tc.at {
				t.Errorf("duplicate = key %q at %q, want key %q at %q", dup.Key, dup.Pointer, tc.key, tc.at)
			}
		})
	}
}

func TestDuplicateKeyErrorNamesTheKeyAndItsPointer(t *testing.T) {
	err := jsonstrict.CheckNoDuplicateKeys([]byte(`{"type":"a","type":"b"}`))
	if got, want := fmt.Sprint(err), `duplicate key "type" at /`; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	err = jsonstrict.CheckNoDuplicateKeys([]byte(`{"data":{"fields":{"k":1,"k":2}}}`))
	if got, want := fmt.Sprint(err), `duplicate key "k" at /data/fields`; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestCheckNoDuplicateKeysRefusesWhatIsNotOneJSONValue(t *testing.T) {
	for _, doc := range []string{``, `{`, `{"a":1`, `{"a":}`, `{"a":1}{"a":2}`, `{"a":1} 2`, `[1,]`, `nope`} {
		err := jsonstrict.CheckNoDuplicateKeys([]byte(doc))
		if err == nil {
			t.Errorf("CheckNoDuplicateKeys(%q) = nil, want an error", doc)
			continue
		}
		var dup *jsonstrict.DuplicateKeyError
		if errors.As(err, &dup) {
			t.Errorf("CheckNoDuplicateKeys(%q) = %v, want a syntax error, not a duplicate", doc, err)
		}
	}
}

// A document nested far deeper than the stack of a recursive walk could hold
// is checked in constant stack and without building a pointer per level.
func TestCheckNoDuplicateKeysDeepNesting(t *testing.T) {
	const depth = 200000
	doc := strings.Repeat("[", depth) + strings.Repeat("]", depth)
	if err := jsonstrict.CheckNoDuplicateKeys([]byte(doc)); err != nil {
		t.Fatalf("deeply nested arrays: %v", err)
	}
	doc = strings.Repeat(`{"a":`, depth) + "1" + strings.Repeat("}", depth)
	if err := jsonstrict.CheckNoDuplicateKeys([]byte(doc)); err != nil {
		t.Fatalf("deeply nested objects: %v", err)
	}
}

// node is a generated JSON value: an object with distinct keys, an array, or
// a scalar.
type node struct {
	obj      bool
	arr      bool
	keys     []string // object keys, unique, in written order
	children []*node  // an object's values, or an array's items
	scalar   string   // a scalar's JSON text
	pointer  string   // the pointer of this node, for objects
}

var keyAlphabet = []rune{'a', 'b', 'c', 'A', 'B', '/', '~', '"', '\\', '\n', 'é', '世', '\'', ' '}

func drawKey(t *rapid.T) string {
	return rapid.StringOfN(rapid.SampledFrom(keyAlphabet), 0, 4, -1).Draw(t, "key")
}

func genNode(t *rapid.T, pointer string, depth int) *node {
	kind := rapid.IntRange(0, 2).Draw(t, "kind")
	if depth >= 4 {
		kind = 2
	}
	switch kind {
	case 0:
		n := &node{obj: true, pointer: pointer}
		seen := map[string]bool{}
		for range rapid.IntRange(0, 4).Draw(t, "members") {
			k := drawKey(t)
			if seen[k] {
				continue
			}
			seen[k] = true
			n.keys = append(n.keys, k)
			n.children = append(n.children, genNode(t, pointer+"/"+escapeToken(k), depth+1))
		}
		return n
	case 1:
		n := &node{arr: true}
		for i := range rapid.IntRange(0, 3).Draw(t, "items") {
			n.children = append(n.children, genNode(t, pointer+"/"+strconv.Itoa(i), depth+1))
		}
		return n
	}
	return &node{scalar: rapid.SampledFrom([]string{`1`, `"s"`, `null`, `true`, `1.5e3`}).Draw(t, "scalar")}
}

func escapeToken(k string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(k)
}

// writeKey writes k as a JSON string, in one of two spellings: the plain
// escapes, or every character as a \u escape, so the same key is spelled two
// ways.
func writeKey(sb *strings.Builder, k string, spell int) {
	sb.WriteByte('"')
	for _, r := range k {
		switch {
		case spell == 1 && r < 0x10000:
			fmt.Fprintf(sb, `\u%04x`, r)
		case r == '"' || r == '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r == '\n':
			sb.WriteString(`\n`)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
}

func (n *node) write(sb *strings.Builder, spell func() int) {
	switch {
	case n.obj:
		sb.WriteByte('{')
		for i, k := range n.keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeKey(sb, k, spell())
			sb.WriteByte(':')
			n.children[i].write(sb, spell)
		}
		sb.WriteByte('}')
	case n.arr:
		sb.WriteByte('[')
		for i, c := range n.children {
			if i > 0 {
				sb.WriteByte(',')
			}
			c.write(sb, spell)
		}
		sb.WriteByte(']')
	default:
		sb.WriteString(n.scalar)
	}
}

func (n *node) objects(out *[]*node) {
	if n.obj {
		*out = append(*out, n)
	}
	for _, c := range n.children {
		c.objects(out)
	}
}

// Property: an object built with unique keys at every level passes however its
// keys are spelled; the same document with one key repeated, at any level and
// under another spelling, fails and names that key and that object.
func TestCheckNoDuplicateKeysProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		root := &node{obj: true, keys: []string{"root"}, children: []*node{genNode(t, "/root", 1)}}
		// A second member, so the root is not always a single-key object.
		root.keys = append(root.keys, "other")
		root.children = append(root.children, genNode(t, "/other", 1))
		spell := func() int { return rapid.IntRange(0, 1).Draw(t, "spelling") }

		var clean strings.Builder
		root.write(&clean, spell)
		if err := jsonstrict.CheckNoDuplicateKeys([]byte(clean.String())); err != nil {
			t.Fatalf("a document with unique keys failed: %v\n%s", err, clean.String())
		}

		var all, objs []*node
		root.objects(&all)
		for _, o := range all {
			if len(o.keys) > 0 {
				objs = append(objs, o) // the root always has keys
			}
		}
		holder := rapid.SampledFrom(objs).Draw(t, "holder")
		key := rapid.SampledFrom(holder.keys).Draw(t, "repeated")
		holder.keys = append(holder.keys, key)
		holder.children = append(holder.children, &node{scalar: `0`})

		var broken strings.Builder
		root.write(&broken, spell)
		err := jsonstrict.CheckNoDuplicateKeys([]byte(broken.String()))
		var dup *jsonstrict.DuplicateKeyError
		if !errors.As(err, &dup) {
			t.Fatalf("a repeated key %q was not found: %v\n%s", key, err, broken.String())
		}
		if dup.Key != key || dup.Pointer != holder.pointer {
			t.Fatalf("duplicate = key %q at %q, want key %q at %q\n%s", dup.Key, dup.Pointer, key, holder.pointer, broken.String())
		}
	})
}

package source

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
)

// UnknownKeys scans raw (one JSON object) and returns the dotted path of
// every object key that the struct type t does not declare. Arrays of objects
// add "[]" to the path. Go's JSON decoder drops unknown keys silently; this is
// how a new Claude Code field is noticed. Invalid JSON yields nothing.
//
// Only the levels the struct type declares as objects are opened; the value
// of a key typed as raw JSON or a scalar is never decoded. A key repeated in
// one object counts once, and its last value is the one opened (the behavior a
// map decode has). Keys match the declared name exactly: "Type" is unknown
// even though a struct decode fills the field "type" from it.
func UnknownKeys(raw []byte, t reflect.Type, prefix string) []string {
	if !json.Valid(raw) {
		return nil
	}
	return UnknownKeysValid(raw, t, prefix)
}

// UnknownKeysValid is UnknownKeys for a raw the caller already knows is valid
// JSON (a successful json.Unmarshal of it, or a slice of such a document), so
// it does not validate again. It is a single hand scan: no map decode and no
// copy of any value, so a very large value costs one pass to skip.
func UnknownKeysValid(raw []byte, t reflect.Type, prefix string) []string {
	var out []string
	scanUnknown(raw, t, prefix, &out)
	sort.Strings(out)
	return out
}

// ArrayElements returns the elements of the JSON array raw (each a subslice of
// raw, no copy) for a raw already known to be valid JSON. ok is false when raw
// is not an array.
func ArrayElements(raw []byte) (elems [][]byte, ok bool) {
	i := skipWS(raw, 0)
	if i >= len(raw) || raw[i] != '[' {
		return nil, false
	}
	i++
	for {
		i = skipWS(raw, i)
		if i >= len(raw) || raw[i] == ']' {
			return elems, true
		}
		if raw[i] == ',' {
			i++
			continue
		}
		end, ok := skipValue(raw, i)
		if !ok {
			return nil, false
		}
		elems = append(elems, raw[i:end])
		i = end
	}
}

type member struct{ key, val []byte }

func scanUnknown(raw []byte, t reflect.Type, path string, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	i := skipWS(raw, 0)
	if i >= len(raw) {
		return
	}
	if t.Kind() == reflect.Slice {
		et := t.Elem()
		for et.Kind() == reflect.Pointer {
			et = et.Elem()
		}
		if _, ok := structOf(et); !ok && et.Kind() != reflect.Slice {
			return
		}
		elems, ok := ArrayElements(raw)
		if !ok {
			return
		}
		for _, e := range elems {
			scanUnknown(e, t.Elem(), path+"[]", out)
		}
		return
	}
	st, ok := structOf(t)
	if !ok || raw[i] != '{' {
		return
	}
	var buf [24]member
	ms := buf[:0]
	i++
	for {
		i = skipWS(raw, i)
		if i >= len(raw) || raw[i] == '}' {
			break
		}
		if raw[i] == ',' {
			i++
			continue
		}
		if raw[i] != '"' {
			return
		}
		ke, ok := skipString(raw, i)
		if !ok {
			return
		}
		key := decodeKey(raw[i:ke])
		i = skipWS(raw, ke)
		if i >= len(raw) || raw[i] != ':' {
			return
		}
		i = skipWS(raw, i+1)
		ve, ok := skipValue(raw, i)
		if !ok {
			return
		}
		ms = append(ms, member{key, raw[i:ve]})
		i = ve
	}
	known := planOf(st).known
	for idx, m := range ms {
		dup := false
		for _, later := range ms[idx+1:] {
			if bytes.Equal(later.key, m.key) {
				dup = true
				break
			}
		}
		if dup {
			continue // last value wins
		}
		ft, isKnown := known[string(m.key)]
		if !isKnown {
			p := string(m.key)
			if path != "" {
				p = path + "." + p
			}
			*out = append(*out, p)
			continue
		}
		p := string(m.key)
		if path != "" {
			p = path + "." + p
		}
		scanUnknown(m.val, ft, p, out)
	}
}

// decodeKey returns the unescaped key of the quoted JSON string q. A plain
// ASCII key is a subslice of q; anything else goes through the decoder so
// escapes and invalid UTF-8 come out exactly as a map decode would give them.
func decodeKey(q []byte) []byte {
	body := q[1 : len(q)-1]
	plain := true
	for _, c := range body {
		if c == '\\' || c >= 0x80 {
			plain = false
			break
		}
	}
	if plain {
		return body
	}
	var s string
	if json.Unmarshal(q, &s) != nil {
		return body
	}
	return []byte(s)
}

func skipWS(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// skipString returns the index just past the string that opens at b[i].
func skipString(b []byte, i int) (int, bool) {
	j := i + 1
	for {
		k := bytes.IndexByte(b[j:], '"')
		if k < 0 {
			return 0, false
		}
		q := j + k
		n := 0
		for p := q - 1; p > i && b[p] == '\\'; p-- {
			n++
		}
		if n%2 == 0 {
			return q + 1, true
		}
		j = q + 1
	}
}

// skipValue returns the index just past the value that starts at b[i].
func skipValue(b []byte, i int) (int, bool) {
	if i >= len(b) {
		return 0, false
	}
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				e, ok := skipString(b, j)
				if !ok {
					return 0, false
				}
				j = e - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1, true
				}
			}
		}
		return 0, false
	}
	j := i
	for j < len(b) {
		switch b[j] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			return j, j > i
		}
		j++
	}
	return j, j > i
}

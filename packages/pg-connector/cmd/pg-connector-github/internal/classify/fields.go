package classify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"
)

// Field is one changed field of a row. Before and After are JSON values; a
// JSON null, or a missing side, is the empty RawMessage.
type Field struct {
	Name   string          `json:"name"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

// wireField is Field's JSON form: the same names, with an empty side spelled
// null (an empty RawMessage is not valid JSON on its own).
type wireField struct {
	Name   string          `json:"name"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

// MarshalJSON writes a missing side as null.
func (f Field) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireField{Name: f.Name, Before: nullIfEmpty(f.Before), After: nullIfEmpty(f.After)})
}

// UnmarshalJSON reads a null or absent side as the empty RawMessage and keeps
// the other values in compact form.
func (f *Field) UnmarshalJSON(b []byte) error {
	var w wireField
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	f.Name, f.Before, f.After = w.Name, tidy(w.Before), tidy(w.After)
	return nil
}

func nullIfEmpty(r json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(r)) == 0 {
		return json.RawMessage("null")
	}
	return r
}

// tidy returns r compacted, or nil when r is empty or the JSON null.
func tidy(r json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(r)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, t); err != nil {
		return slices.Clone(t)
	}
	return json.RawMessage(buf.Bytes())
}

// EncodeFields is the stored form of Result.Fields: a JSON array, [] for none.
func EncodeFields(fs []Field) (json.RawMessage, error) {
	if len(fs) == 0 {
		return json.RawMessage("[]"), nil
	}
	for _, f := range fs {
		for _, side := range []json.RawMessage{f.Before, f.After} {
			if len(bytes.TrimSpace(side)) > 0 && !json.Valid(side) {
				return nil, fmt.Errorf("classify: field %q holds a value that is not JSON", f.Name)
			}
		}
	}
	raw, err := json.Marshal(fs)
	if err != nil {
		return nil, fmt.Errorf("classify: encode fields: %w", err)
	}
	return raw, nil
}

// DecodeFields reads the stored form back. An empty or null input is no fields.
func DecodeFields(raw json.RawMessage) ([]Field, error) {
	if tidy(raw) == nil {
		return nil, nil
	}
	var fs []Field
	if err := json.Unmarshal(raw, &fs); err != nil {
		return nil, fmt.Errorf("classify: decode fields: %w", err)
	}
	if fs == nil {
		fs = []Field{}
	}
	return fs, nil
}

// normalize returns fs without the fields whose after equals their before,
// with compact values, sorted by name.
func normalize(fs []Field) []Field {
	var out []Field
	for _, f := range fs {
		f.Before, f.After = tidy(f.Before), tidy(f.After)
		if bytes.Equal(f.Before, f.After) {
			continue
		}
		out = append(out, f)
	}
	slices.SortStableFunc(out, func(a, b Field) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		}
		return 0
	})
	return out
}

// builder collects the kinds and fields of one classification.
type builder struct {
	kinds  []string
	fields []Field
}

func (b *builder) kind(k string) { b.kinds = append(b.kinds, k) }

func (b *builder) field(name string, before, after json.RawMessage) {
	b.fields = append(b.fields, Field{Name: name, Before: before, After: after})
}

func (b *builder) result() Result {
	return Result{Kinds: orderKinds(b.kinds), Fields: normalize(b.fields)}
}

// jsonOf encodes v; the values passed are strings, numbers, bools and small
// structs of those, which always encode.
func jsonOf(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic("classify: marshal: " + err.Error())
	}
	return raw
}

// diff records a field when its value moved and reports whether it did.
func diff[T comparable](b *builder, name string, before, after T) bool {
	if before == after {
		return false
	}
	b.field(name, jsonOf(before), jsonOf(after))
	return true
}

// diffSet records a field holding a list compared as a set (the lists are
// sorted copies, so a reordered answer is not a change).
func diffSet(b *builder, name string, before, after []string) bool {
	x, y := sortedCopy(before), sortedCopy(after)
	if slices.Equal(x, y) {
		return false
	}
	b.field(name, jsonOf(x), jsonOf(y))
	return true
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return out
}

// textHash is the hex SHA-256 of a text: the digest a capped body carries so a
// consumer can tell two long texts that share a prefix apart.
func textHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// capText returns s cut to at most limit bytes, backing up to a character
// boundary so the cut text stays valid UTF-8 and never exceeds limit. A limit
// of zero or less keeps no text at all (the hash still identifies it).
func capText(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// bodyValue is the JSON object a text field holds: the text capped at limit
// bytes and the hash of the full text.
func bodyValue(s string, limit int) json.RawMessage {
	return jsonOf(struct {
		Body string `json:"body"`
		Hash string `json:"hash"`
	}{capText(s, limit), textHash(s)})
}

// diffBody records a text field (a PR body, a comment body) when the full text
// moved, even if the capped texts are equal.
func diffBody(b *builder, name, before, after string, limit int) bool {
	if before == after {
		return false
	}
	b.field(name, bodyValue(before, limit), bodyValue(after, limit))
	return true
}

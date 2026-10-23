package classify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
)

// Field is one changed field of a row (the shared engine's type, re-exported).
type Field = engine.Field

// EncodeFields is the stored form of Result.Fields: a JSON array, [] for none.
func EncodeFields(fs []Field) (json.RawMessage, error) { return engine.EncodeFields(fs) }

// DecodeFields reads the stored form back. An empty or null input is no fields.
func DecodeFields(raw json.RawMessage) ([]Field, error) { return engine.DecodeFields(raw) }

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
	return Result{Kinds: engine.OrderKinds(Kinds(), b.kinds), Fields: engine.Normalize(b.fields)}
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

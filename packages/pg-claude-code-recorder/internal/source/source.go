// Package source is the contract every recorder source implements, and the
// helpers a source uses to turn a decoded Go struct into indexed fields.
//
// A source is compiled in. Its record structs carry `ingest:"<kind>"` tags
// that name each field's index type; a field without a tag is known (a key the
// parser expects) but not indexed. The recorder's logic is limited to direct
// reads of a field from a file (ADR 0015): nothing here interprets a value.
package source

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Mode is how a source's files are copied.
type Mode string

// Copy modes.
const (
	Append   Mode = "append"
	Snapshot Mode = "snapshot"
)

// Kind is a field's index type.
type Kind string

// Field kinds (the values of the ingest tag).
const (
	KindID      Kind = "id"
	KindKeyword Kind = "keyword"
	KindPath    Kind = "path"
	KindInt     Kind = "int"
	KindFloat   Kind = "float"
	KindBool    Kind = "bool"
	KindTime    Kind = "time"
	KindText    Kind = "text"
	KindOpaque  Kind = "opaque"
	// KindSession is an id that is also the record's session key.
	KindSession Kind = "session"
)

// Searchable reports whether a kind is indexed for search (and then is not
// stored as a typed field).
func (k Kind) Searchable() bool { return k == KindText || k == KindOpaque }

// Captures are the named parts of a path a source matched, such as project,
// session and agent.
type Captures map[string]string

// Field is one indexed fact of a record. S holds strings (and the value of a
// text or opaque field), N integers, bools (0 or 1) and times (unix
// nanoseconds), F floats.
type Field struct {
	Name string
	Kind Kind
	S    string
	N    int64
	F    float64
}

// ItemLine is the Item of a record that stands for a whole line; a child record
// carries the zero-based index of its content item.
const ItemLine = -1

// Record is what a parser yields for one line (and one per child item).
type Record struct {
	Type    string
	Item    int
	Session string
	// Time is the record's own timestamp; zero when the line has none (the
	// parser then uses the time the recorder saw the bytes).
	Time    time.Time
	Fields  []Field
	Unknown []string // dotted paths of keys the parser did not recognize
}

// Source is the contract every recorder source implements.
type Source interface {
	Name() string
	Version() int                       // bump to re-parse stored copies
	Mode() Mode                         // Append or Snapshot
	Match(path string) (Captures, bool) // e.g. project, session, agent from the path
	Parse(c Captures, raw []byte) ([]Record, error)
}

// Catalogued is implemented by a source that lists the fields it can produce.
type Catalogued interface {
	Catalog() map[string]Kind
}

// Snake converts a JSON key to its snake_case query name.
func Snake(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := rs[i-1]
				nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func jsonName(sf reflect.StructField) string {
	tag := sf.Tag.Get("json")
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return ""
	}
	if name == "" {
		return sf.Name
	}
	return name
}

func queryName(sf reflect.StructField, prefix string) string {
	if f := sf.Tag.Get("field"); f != "" {
		return f
	}
	n := Snake(jsonName(sf))
	if prefix != "" {
		return prefix + "." + n
	}
	return n
}

func structOf(t reflect.Type) (reflect.Type, bool) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct && t != reflect.TypeOf(time.Time{}) {
		return t, true
	}
	return nil, false
}

// Extract returns the indexed fields of the struct v (or pointer to one),
// naming each by the snake_case form of its JSON key with dots for nesting
// (prefix is prepended). Absent values (nil pointers, empty strings) yield no
// field.
func Extract(v any, prefix string) []Field {
	var out []Field
	extract(reflect.ValueOf(v), prefix, &out)
	return out
}

func extract(rv reflect.Value, prefix string, out *[]Field) {
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if !sf.IsExported() || jsonName(sf) == "" {
			continue
		}
		fv := rv.Field(i)
		kind := Kind(sf.Tag.Get("ingest"))
		if kind == "" {
			if _, ok := structOf(sf.Type); ok {
				extract(fv, queryName(sf, prefix), out)
			}
			continue
		}
		if f, ok := scalar(queryName(sf, prefix), kind, fv); ok {
			*out = append(*out, f)
		}
	}
}

func scalar(name string, kind Kind, fv reflect.Value) (Field, bool) {
	for fv.Kind() == reflect.Ptr {
		if fv.IsNil() {
			return Field{}, false
		}
		fv = fv.Elem()
	}
	f := Field{Name: name, Kind: kind}
	switch kind {
	case KindID, KindKeyword, KindPath, KindText, KindOpaque, KindSession:
		var s string
		switch {
		case fv.Type() == reflect.TypeOf(json.RawMessage(nil)):
			raw := bytes.TrimSpace(fv.Bytes())
			if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
				return f, false
			}
			s = string(raw)
		case fv.Kind() == reflect.String:
			s = fv.String()
		default:
			return f, false
		}
		if s == "" {
			return f, false
		}
		f.S = s
	case KindInt:
		switch fv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			f.N = fv.Int()
		case reflect.Float32, reflect.Float64:
			f.N = int64(fv.Float())
		default:
			return f, false
		}
	case KindFloat:
		switch fv.Kind() {
		case reflect.Float32, reflect.Float64:
			f.F = fv.Float()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			f.F = float64(fv.Int())
		default:
			return f, false
		}
	case KindBool:
		if fv.Kind() != reflect.Bool {
			return f, false
		}
		if fv.Bool() {
			f.N = 1
		}
	case KindTime:
		var t time.Time
		switch {
		case fv.Type() == reflect.TypeOf(time.Time{}):
			t = fv.Interface().(time.Time)
		case fv.Kind() == reflect.String:
			p, ok := ParseTime(fv.String())
			if !ok {
				return f, false
			}
			t = p
		case fv.Kind() == reflect.Int64 || fv.Kind() == reflect.Int:
			t = EpochTime(float64(fv.Int()))
		case fv.Kind() == reflect.Float64:
			t = EpochTime(fv.Float())
		default:
			return f, false
		}
		if t.IsZero() {
			return f, false
		}
		f.N = t.UnixNano()
	default:
		return f, false
	}
	return f, true
}

// ParseTime reads an RFC 3339 timestamp (with or without fractional seconds).
func ParseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// EpochTime reads a unix timestamp in seconds, or in milliseconds when the
// value is too large to be seconds.
func EpochTime(v float64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	if v >= 1e11 {
		return time.UnixMilli(int64(v))
	}
	return time.Unix(int64(v), 0)
}

// Catalog lists the fields the struct types can produce (name to kind).
func Catalog(types ...any) map[string]Kind {
	out := map[string]Kind{}
	for _, t := range types {
		catalog(reflect.TypeOf(t), "", out)
	}
	return out
}

func catalog(rt reflect.Type, prefix string, out map[string]Kind) {
	st, ok := structOf(rt)
	if !ok {
		return
	}
	for i := 0; i < st.NumField(); i++ {
		sf := st.Field(i)
		if !sf.IsExported() || jsonName(sf) == "" {
			continue
		}
		if kind := Kind(sf.Tag.Get("ingest")); kind != "" {
			k := kind
			if k == KindSession {
				k = KindID
			}
			out[queryName(sf, prefix)] = k
			continue
		}
		if _, ok := structOf(sf.Type); ok {
			catalog(sf.Type, queryName(sf, prefix), out)
		}
	}
}

// UnknownKeys decodes raw (one JSON object) and returns the dotted path of
// every object key that the struct type t does not declare, once per
// occurrence. Arrays of objects add "[]" to the path. Go's JSON decoder drops
// unknown keys silently; this is how a new Claude Code field is noticed.
func UnknownKeys(raw []byte, t reflect.Type, prefix string) []string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	var out []string
	unknown(v, t, prefix, &out)
	sort.Strings(out)
	return out
}

func unknown(v any, t reflect.Type, path string, out *[]string) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch tv := v.(type) {
	case map[string]any:
		st, ok := structOf(t)
		if !ok {
			return
		}
		known := map[string]reflect.Type{}
		for i := 0; i < st.NumField(); i++ {
			sf := st.Field(i)
			if n := jsonName(sf); n != "" && sf.IsExported() {
				known[n] = sf.Type
			}
		}
		for k, child := range tv {
			p := k
			if path != "" {
				p = path + "." + k
			}
			ft, ok := known[k]
			if !ok {
				*out = append(*out, p)
				continue
			}
			unknown(child, ft, p, out)
		}
	case []any:
		if t.Kind() != reflect.Slice {
			return
		}
		for _, e := range tv {
			unknown(e, t.Elem(), path+"[]", out)
		}
	}
}

// Itoa is strconv.Itoa for callers that name captures.
func Itoa(i int) string { return strconv.Itoa(i) }

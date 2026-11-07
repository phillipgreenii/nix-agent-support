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
	"strconv"
	"strings"
	"sync"
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
	// Mismatches names the catalogued fields whose value did not convert
	// losslessly to the declared type (see Convert); the parser counts each in
	// field_type_mismatch_total. Nothing in the stored output depends on it.
	Mismatches []string `json:"-"`
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
	for t.Kind() == reflect.Pointer {
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
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return
	}
	plan := planOf(rv.Type())
	for i := range plan.fields {
		fp := &plan.fields[i]
		fv := rv.Field(fp.index)
		if fp.kind == "" {
			if fp.nested {
				extract(fv, fp.name(prefix), out)
			}
			continue
		}
		if f, ok := scalar(fp.name(prefix), fp.kind, fv); ok {
			*out = append(*out, f)
		}
	}
}

func scalar(name string, kind Kind, fv reflect.Value) (Field, bool) {
	for fv.Kind() == reflect.Pointer {
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

// fieldPlan is what Extract needs to know about one exported, JSON-named
// struct field, worked out once per type instead of once per record.
type fieldPlan struct {
	index  int
	kind   Kind   // the ingest tag; empty when the field is not indexed
	nested bool   // an untagged field whose type is a struct (or pointer to one)
	field  string // the field tag override, which ignores any prefix
	snake  string // snake_case JSON key
}

func (fp *fieldPlan) name(prefix string) string {
	if fp.field != "" {
		return fp.field
	}
	if prefix != "" {
		return prefix + "." + fp.snake
	}
	return fp.snake
}

// typePlan is the cached reflection result for one struct type.
type typePlan struct {
	fields []fieldPlan
	known  map[string]reflect.Type // JSON key to field type, for UnknownKeys
}

var plans sync.Map // reflect.Type to *typePlan

func planOf(rt reflect.Type) *typePlan {
	if p, ok := plans.Load(rt); ok {
		return p.(*typePlan)
	}
	p := &typePlan{known: map[string]reflect.Type{}}
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		n := jsonName(sf)
		if !sf.IsExported() || n == "" {
			continue
		}
		fp := fieldPlan{index: i, kind: Kind(sf.Tag.Get("ingest")), field: sf.Tag.Get("field"), snake: Snake(n)}
		if fp.kind == "" {
			_, fp.nested = structOf(sf.Type)
		}
		p.fields = append(p.fields, fp)
		p.known[n] = sf.Type
	}
	actual, _ := plans.LoadOrStore(rt, p)
	return actual.(*typePlan)
}

// Itoa is strconv.Itoa for callers that name captures.
func Itoa(i int) string { return strconv.Itoa(i) }

// Tombstoner is implemented by a snapshot source whose deleted file is itself
// a fact (the session registry): Tombstone returns the deletion record, built
// from the file's last stored version last (nil when none) and the deletion
// time. A source that does not implement it records no deletion.
type Tombstoner interface {
	Tombstone(c Captures, last []byte, at time.Time) (Record, bool)
}

// ItemTombstone is the Item of a deletion record.
const ItemTombstone = -2

// JSONRecord is the shared tail of a JSON source's Parse: it decodes raw into
// v (a pointer to the source's struct), extracts the indexed fields and the
// unknown keys, and returns the one record of type typ.
func JSONRecord(typ, session string, raw []byte, v any) (Record, error) {
	raw = bytes.TrimSpace(raw)
	if err := json.Unmarshal(raw, v); err != nil {
		return Record{}, err
	}
	rec := Record{Type: typ, Item: ItemLine, Session: session}
	rec.Fields = Extract(v, "")
	rec.Unknown = UnknownKeysValid(raw, reflect.TypeOf(v).Elem(), "")
	return rec, nil
}

// Str returns a keyword-style field for a capture value (empty values yield no
// field).
func Str(name string, kind Kind, s string) []Field {
	if s == "" {
		return nil
	}
	return []Field{{Name: name, Kind: kind, S: s}}
}

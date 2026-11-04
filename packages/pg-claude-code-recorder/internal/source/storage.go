package source

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
)

// StorageKind says where a catalogued field's value is stored.
type StorageKind string

// Storage kinds (the value of the store tag, before any colon).
const (
	// StoreFields is the default: a row of the generic fields table.
	StoreFields StorageKind = "fields"
	// StoreRecord is a typed column of the records table.
	StoreRecord StorageKind = "record"
	// StoreExt is a typed column of a one-to-one extension table.
	StoreExt StorageKind = "ext"
)

// Storage is a field's storage declaration: `fields` (the default),
// `record:<column>` or `ext:<table>.<column>`. It only declares; nothing
// routes by it until the schema that has those columns lands.
type Storage struct {
	Kind   StorageKind
	Table  string // StoreExt only
	Column string // StoreRecord and StoreExt
}

// String is the tag form of the declaration.
func (s Storage) String() string {
	switch s.Kind {
	case StoreRecord:
		return "record:" + s.Column
	case StoreExt:
		return "ext:" + s.Table + "." + s.Column
	}
	return string(StoreFields)
}

func validIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// ParseStorage reads a store tag value; the empty string is `fields`.
func ParseStorage(s string) (Storage, error) {
	switch {
	case s == "" || s == string(StoreFields):
		return Storage{Kind: StoreFields}, nil
	case strings.HasPrefix(s, "record:"):
		col := strings.TrimPrefix(s, "record:")
		if !validIdent(col) {
			return Storage{}, fmt.Errorf("storage %q: bad column", s)
		}
		return Storage{Kind: StoreRecord, Column: col}, nil
	case strings.HasPrefix(s, "ext:"):
		tbl, col, ok := strings.Cut(strings.TrimPrefix(s, "ext:"), ".")
		if !ok || !validIdent(tbl) || !validIdent(col) {
			return Storage{}, fmt.Errorf("storage %q: want ext:<table>.<column>", s)
		}
		return Storage{Kind: StoreExt, Table: tbl, Column: col}, nil
	}
	return Storage{}, fmt.Errorf("storage %q: want fields, record:<column> or ext:<table>.<column>", s)
}

// CatalogStorage lists the storage declaration of every field Catalog lists
// (same names): a field without a store tag is `fields`. It reports an
// error for an unparseable tag, a store tag on a field that has no ingest
// kind, a record or ext declaration on a searchable (text or opaque) field, or
// two fields declared into the same column.
func CatalogStorage(types ...any) (map[string]Storage, error) {
	out := map[string]Storage{}
	cols := map[string]string{}
	for _, t := range types {
		if err := catalogStorage(reflect.TypeOf(t), "", out, cols); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func catalogStorage(rt reflect.Type, prefix string, out map[string]Storage, cols map[string]string) error {
	st, ok := structOf(rt)
	if !ok {
		return nil
	}
	for i := 0; i < st.NumField(); i++ {
		sf := st.Field(i)
		if !sf.IsExported() || jsonName(sf) == "" {
			continue
		}
		name := queryName(sf, prefix)
		tag, hasTag := sf.Tag.Lookup("store")
		if kind := Kind(sf.Tag.Get("ingest")); kind != "" {
			s, err := ParseStorage(tag)
			if err != nil {
				return fmt.Errorf("field %s: %w", name, err)
			}
			if s.Kind != StoreFields && kind.Searchable() {
				return fmt.Errorf("field %s: a %s field cannot use %s storage", name, kind, s)
			}
			if s.Kind != StoreFields {
				key := s.Table + "." + s.Column
				if s.Kind == StoreRecord {
					key = "record." + s.Column
				}
				if prev, dup := cols[key]; dup && prev != name {
					return fmt.Errorf("fields %s and %s both declare %s", prev, name, s)
				}
				cols[key] = name
			}
			out[name] = s
			continue
		}
		if hasTag {
			return fmt.Errorf("field %s: store tag without an ingest kind", name)
		}
		if _, ok := structOf(sf.Type); ok {
			if err := catalogStorage(sf.Type, name, out, cols); err != nil {
				return err
			}
		}
	}
	return nil
}

// Converted is the typed value Convert produced: S for string-like kinds, N
// for int, bool (0 or 1) and time (unix nanoseconds), F for float.
type Converted struct {
	S string
	N int64
	F float64
}

const maxNumberLiteral = 64

// intLiteral reads a JSON number literal as an int64 only when it is integral
// and in range ("3", "3.0" and "3e2" qualify, "3.5" and 2^63 do not).
func intLiteral(lit string) (int64, bool) {
	if len(lit) > maxNumberLiteral {
		return 0, false
	}
	if n, err := strconv.ParseInt(lit, 10, 64); err == nil {
		return n, true
	}
	r, ok := new(big.Rat).SetString(lit)
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, false
	}
	return r.Num().Int64(), true
}

// Convert applies the lossless-conversion rule: it turns the JSON value raw
// into the typed form of a field of the given kind, or reports ok=false when
// that cannot be done without truncating, rounding or clamping. A caller that
// gets ok=false MUST keep the raw value as text in the generic fields row,
// leave the typed column NULL and count a field type mismatch.
//
// Accepted: an integral number within int64 for an int; a decimal string that
// re-formats to the identical string for an int or float; a number for a float
// that fits a float64; a JSON string for the string kinds; a JSON bool for a
// bool; an RFC 3339 string or an integral epoch number for a time. Text and
// opaque kinds are searched, not converted, so they are never accepted.
func Convert(kind Kind, raw json.RawMessage) (Converted, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return Converted{}, false
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&v) != nil || dec.More() {
		return Converted{}, false
	}
	switch kind {
	case KindID, KindKeyword, KindPath, KindSession:
		if s, ok := v.(string); ok && s != "" {
			return Converted{S: s}, true
		}
	case KindInt:
		switch x := v.(type) {
		case json.Number:
			if n, ok := intLiteral(x.String()); ok {
				return Converted{N: n}, true
			}
		case string:
			if n, err := strconv.ParseInt(x, 10, 64); err == nil && strconv.FormatInt(n, 10) == x {
				return Converted{N: n}, true
			}
		}
	case KindFloat:
		switch x := v.(type) {
		case json.Number:
			if f, err := strconv.ParseFloat(x.String(), 64); err == nil && !math.IsInf(f, 0) {
				return Converted{F: f}, true
			}
		case string:
			if f, err := strconv.ParseFloat(x, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) && strconv.FormatFloat(f, 'f', -1, 64) == x {
				return Converted{F: f}, true
			}
		}
	case KindBool:
		if b, ok := v.(bool); ok {
			if b {
				return Converted{N: 1}, true
			}
			return Converted{}, true
		}
	case KindTime:
		switch x := v.(type) {
		case string:
			if t, ok := ParseTime(x); ok && !t.IsZero() && t.Year() > 1700 && t.Year() < 2200 {
				return Converted{N: t.UnixNano()}, true
			}
		case json.Number:
			if n, ok := intLiteral(x.String()); ok && n > 0 {
				if t := EpochTime(float64(n)); !t.IsZero() && t.Year() > 1700 && t.Year() < 2200 {
					return Converted{N: t.UnixNano()}, true
				}
			}
		}
	}
	return Converted{}, false
}

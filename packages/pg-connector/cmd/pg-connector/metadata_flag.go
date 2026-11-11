package main

import (
	"encoding/csv"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/pflag"
)

// metadataFlag is the --metadata flag value: a repeatable string-to-string
// map. It replaces pflag's StringToStringVar, whose Set only CSV-decodes a
// token when it holds two or more "=" and otherwise merely trims the
// surrounding quotes: a quoted token such as `"k={""x"":1,""y"":2}"` (one
// "=", the shape RFC 4180 quoting gives a JSON value that contains commas)
// kept its doubled quotes in the stored value.
//
// A token is CSV-decoded (one record, comma-separated key=value fields, as
// pflag does) when it starts with a double quote or holds two or more "=";
// any other token is a single pair taken verbatim, split on the first "=",
// so an unquoted `k={"x":1}` still works.
type metadataFlag struct {
	value   *map[string]string
	changed bool
}

func newMetadataFlag(p *map[string]string) *metadataFlag {
	*p = nil
	return &metadataFlag{value: p}
}

func (m *metadataFlag) Set(val string) error {
	var fields []string
	if strings.HasPrefix(val, `"`) || strings.Count(val, "=") >= 2 {
		rec, err := csv.NewReader(strings.NewReader(val)).Read()
		if err != nil {
			return err
		}
		fields = rec
	} else {
		fields = []string{val}
	}
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return fmt.Errorf("%s must be formatted as key=value", f)
		}
		out[k] = v
	}
	if !m.changed || *m.value == nil {
		*m.value = out
	} else {
		for k, v := range out {
			(*m.value)[k] = v
		}
	}
	m.changed = true
	return nil
}

func (m *metadataFlag) Type() string { return "stringToString" }

func (m *metadataFlag) String() string {
	keys := make([]string, 0, len(*m.value))
	for k := range *m.value {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+(*m.value)[k])
	}
	return "[" + strings.Join(parts, ",") + "]"
}

var _ pflag.Value = (*metadataFlag)(nil)

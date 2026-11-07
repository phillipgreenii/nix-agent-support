package source

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// oracleUnknownKeys is the map-decode implementation UnknownKeys replaced
// (json.Valid, then a map decode at every declared level). It is the
// behavioral reference: the hand scanner MUST agree with it on every input.
func oracleUnknownKeys(raw []byte, t reflect.Type, prefix string) []string {
	if !json.Valid(raw) {
		return nil
	}
	var out []string
	oracleUnknown(raw, t, prefix, &out)
	sort.Strings(out)
	return out
}

func oracleUnknown(raw json.RawMessage, t reflect.Type, path string, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t.Kind() == reflect.Slice:
		et := t.Elem()
		for et.Kind() == reflect.Pointer {
			et = et.Elem()
		}
		if _, ok := structOf(et); !ok && et.Kind() != reflect.Slice {
			return
		}
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return
		}
		for _, e := range items {
			oracleUnknown(e, t.Elem(), path+"[]", out)
		}
	default:
		st, ok := structOf(t)
		if !ok {
			return
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return
		}
		known := planOf(st).known
		for k, child := range obj {
			p := k
			if path != "" {
				p = path + "." + k
			}
			ft, ok := known[k]
			if !ok {
				*out = append(*out, p)
				continue
			}
			oracleUnknown(child, ft, p, out)
		}
	}
}

type deep struct {
	A     *deep           `json:"a"`
	List  []deep          `json:"list"`
	Grid  [][]inner       `json:"grid"`
	Ptrs  []*inner        `json:"ptrs"`
	Nums  []int           `json:"nums"`
	Raw   json.RawMessage `json:"raw"`
	Inner inner           `json:"inner"`
}

func TestUnknownKeysAgreesWithMapDecodeOracle(t *testing.T) {
	inputs := []string{
		``, ` `, `null`, `5`, `"s"`, `[]`, `{}`, ` { } `,
		`{"extra":`, `{"extra":1,}`, `{"a" 1}`, `{"a":1 "b":2}`, `{"extra":1} x`, `{"x":tru}`,
		`{"extra":1,"extra":2}`,
		`{"usage":{"output_tokens":1,"n":1},"usage":{"n":2}}`,
		`{"usage":{"first":1},"usage":{"second":2}}`,
		`{"usage":{"first":1},"usage":null}`,
		`{"usage":null,"usage":{"second":2}}`,
		`{"Usage":{"output_tokens":1},"PARENTUUID":"x","parentuuid":"y"}`,
		`{"parent\u0055uid":"x","par\u0065ntUuid":"y","ex\u0074ra":1,"e\"x":1,"e\\x":2}`,
		"{\"caf\xc3\xa9\":1,\"bad\xff\":2,\"\":3}",
		`{"usage":"text","items":{"a":1},"raw":{"deep":{"x":1}}}`,
		`{"usage":[1,2],"items":"s","items":[{"zzz":1},null,5,"x",[],{"output_tokens":1,"q":1}]}`,
		`{"items":[{"a":1},{"a":2},{"b":[{"c":1}]}]}`,
		`{"a":{"a":{"a":{"zzz":1}},"list":[{"y":1},{"list":[{"w":1}]}]},"list":[{"k":1},{"k":2}]}`,
		`{"grid":[[{"u":1},{"output_tokens":2}],[],[{"v":1}],null,{"w":1}]}`,
		`{"ptrs":[null,{"p":1},{"output_tokens":1}],"nums":[1,2,{"a":1}]}`,
		`{"raw":"a \"quoted\" \\ string {\"not\":1} [","inner":{"s":"}{","t":"\\\\"},"zz":"]"}`,
		`{"raw":{"nested":[{"a":"}"},"]",1.5e-3,true,false,null]},"extra" : [ 1 , { "x" : 1 } ] }`,
		"{\n\t\"extra\"\r:\n1,\n\"usage\" : { \"n\" : 1 }\n}",
		`{"inner":{"output_tokens":1,"n":2},"inner":{"m":3}}`,
		`{"list":[{"a":{"zz":1}},{"a":null},{"a":{"zz":2}}]}`,
		`{"list":[{"zz":1},{"zz":1}]}`,
		`[{"extra":1}]`,
	}
	types := []reflect.Type{reflect.TypeOf(outer{}), reflect.TypeOf(deep{}), reflect.TypeOf(&outer{}).Elem()}
	for _, in := range inputs {
		for _, typ := range types {
			for _, prefix := range []string{"", "message.content[]"} {
				want := oracleUnknownKeys([]byte(in), typ, prefix)
				got := UnknownKeys([]byte(in), typ, prefix)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s %q prefix %q: got %#v want %#v", typ, in, prefix, got, want)
				}
				if json.Valid([]byte(in)) {
					if got := UnknownKeysValid([]byte(in), typ, prefix); !reflect.DeepEqual(got, want) {
						t.Errorf("valid %s %q prefix %q: got %#v want %#v", typ, in, prefix, got, want)
					}
				}
			}
		}
	}
}

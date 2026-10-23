package rolefile

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// SecretLookingKey reports whether a sessionEnv key name looks like it carries
// a secret: its name contains TOKEN, SECRET, KEY or PASSWORD. The match is a
// case-insensitive substring match, a deliberate choice: env names are
// conventionally upper-case, but a lower-case "gh_token" leaks just as much, and
// a false positive costs only a warning (the design makes this a warning, not a
// problem).
func SecretLookingKey(k string) bool {
	u := strings.ToUpper(k)
	for _, frag := range []string{"TOKEN", "SECRET", "KEY", "PASSWORD"} {
		if strings.Contains(u, frag) {
			return true
		}
	}
	return false
}

func SortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TopLevelExtras are role-file keys that RoleFile does not decode but that are
// real: survivesShutdown is rendered into every ccpool role's JSON by the nix
// module and read by pg-router core, which ignores nothing else of it. The Go
// RoleFile ignores it, so a strict check MUST accept it (as a boolean).
var TopLevelExtras = map[string]reflect.Type{
	"survivesShutdown": reflect.TypeOf(false),
}

// ShapeChecker walks the generic decoded JSON against RoleFile's Go types and
// records an unknown key or a wrong type anywhere as a problem naming the
// field, without stopping at the first. A key matches a field the way
// encoding/json does: by its json tag name, else by its Go field name, either
// way case-insensitively (roles.IsolationConfig has no tags).
type ShapeChecker struct {
	Problems  []string
	TypeError bool
}

var textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()

func (s *ShapeChecker) typeProblem(path, want string, got any) {
	s.TypeError = true
	s.Problems = append(s.Problems, fmt.Sprintf("%s: want %s, got %s", path, want, jsonKind(got)))
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case json.Number:
		return "a number"
	case string:
		return "a string"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

func (s *ShapeChecker) Walk(path string, v any, t reflect.Type, extras map[string]reflect.Type) {
	if v == nil {
		return // encoding/json leaves the field alone on null
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	here := path
	if here == "" {
		here = "role config"
	}
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		if _, ok := v.(string); !ok {
			s.typeProblem(here, "a string", v)
		}
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			s.typeProblem(here, "an object", v)
			return
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := f.Name
			if tag := strings.Split(f.Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
				name = tag
			}
			fields[strings.ToLower(name)] = f.Type
		}
		for name, ft := range extras {
			fields[strings.ToLower(name)] = ft
		}
		for _, k := range SortedKeys(obj) {
			ft, known := fields[strings.ToLower(k)]
			if !known {
				s.Problems = append(s.Problems, fmt.Sprintf("%s: unknown key %q", here, k))
				continue
			}
			child := k
			if path != "" {
				child = path + "." + k
			}
			s.Walk(child, obj[k], ft, nil)
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			s.typeProblem(here, "an object", v)
			return
		}
		for _, k := range SortedKeys(obj) {
			s.Walk(path+"."+k, obj[k], t.Elem(), nil)
		}
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			s.typeProblem(here, "an array", v)
			return
		}
		for i, e := range arr {
			s.Walk(fmt.Sprintf("%s[%d]", path, i), e, t.Elem(), nil)
		}
	case reflect.Bool:
		if _, ok := v.(bool); !ok {
			s.typeProblem(here, "a boolean", v)
		}
	case reflect.String:
		if _, ok := v.(string); !ok {
			s.typeProblem(here, "a string", v)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, ok := v.(json.Number)
		if !ok {
			s.typeProblem(here, "an integer", v)
			return
		}
		if i, err := n.Int64(); err != nil || reflect.Zero(t).OverflowInt(i) {
			s.typeProblem(here, "an integer", v)
		}
	default:
		// No other kind occurs in RoleFile; treat it as unchecked rather than guess.
	}
}

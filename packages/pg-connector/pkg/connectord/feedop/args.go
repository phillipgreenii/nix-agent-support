package feedop

import (
	"encoding/json"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// args decodes the arguments of an op into their keys; an absent or null
// argument object is no keys.
func args(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := scriptout.Decode(raw, &m); err != nil {
		return nil, invalid("decode arguments: %v", err)
	}
	return m, nil
}

// stringArg reads an optional string argument (absent or null is empty); a value
// of another type is invalid_argument.
func stringArg(m map[string]json.RawMessage, name string) (string, error) {
	raw, ok := m[name]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", invalid("argument %q must be a string", name)
	}
	return strings.TrimSpace(s), nil
}

// boolArg reads an optional boolean argument (absent or null is false); a value
// of another type is invalid_argument.
func boolArg(m map[string]json.RawMessage, name string) (bool, error) {
	raw, ok := m[name]
	if !ok || string(raw) == "null" {
		return false, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, invalid("argument %q must be a boolean", name)
	}
	return b, nil
}

// WatchArg decodes the boolean argument "watch" of a read or lookup op: raw is
// the op's argument object (a bare boolean is read as the value itself), and an
// absent or null argument is false. Any other type is invalid_argument.
func WatchArg(raw json.RawMessage) (bool, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return false, nil
	}
	if text[0] != '{' {
		var b bool
		if err := json.Unmarshal([]byte(text), &b); err != nil {
			return false, invalid(`argument "watch" must be a boolean`)
		}
		return b, nil
	}
	m, err := args(raw)
	if err != nil {
		return false, err
	}
	return boolArg(m, "watch")
}

// keyArgs reads the two arguments every op of the feed requires.
func keyArgs(m map[string]json.RawMessage) (consumer, query string, err error) {
	if consumer, err = stringArg(m, "consumer"); err != nil {
		return "", "", err
	}
	if query, err = stringArg(m, "query"); err != nil {
		return "", "", err
	}
	if consumer == "" {
		return "", "", invalid(`the argument "consumer" is required`)
	}
	if query == "" {
		return "", "", invalid(`the argument "query" is required`)
	}
	return consumer, query, nil
}

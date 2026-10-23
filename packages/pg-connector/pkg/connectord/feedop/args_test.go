package feedop

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

func TestWatchArg(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		want    bool
		invalid bool
	}{
		{"absent arguments", ``, false, false},
		{"null arguments", `null`, false, false},
		{"empty object", `{}`, false, false},
		{"watch absent", `{"query":"mine"}`, false, false},
		{"watch null", `{"watch":null}`, false, false},
		{"watch true", `{"watch":true}`, true, false},
		{"watch false", `{"watch":false}`, false, false},
		{"a bare true", `true`, true, false},
		{"a bare false", `false`, false, false},
		{"watch a string", `{"watch":"yes"}`, false, true},
		{"watch a number", `{"watch":1}`, false, true},
		{"a bare string", `"yes"`, false, true},
		{"not an object", `[true]`, false, true},
	} {
		got, err := WatchArg(json.RawMessage(tc.raw))
		if tc.invalid {
			if !errors.Is(err, scriptout.ErrInvalidArgument) {
				t.Errorf("%s: err = %v, want invalid_argument", tc.name, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: got (%v, %v), want %v", tc.name, got, err, tc.want)
		}
	}
}

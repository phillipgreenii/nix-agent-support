package broker

import (
	"errors"
	"testing"
)

func TestCheckArgs(t *testing.T) {
	ok := [][]string{
		{"fake", "by-tool", "--limit", "5"},
		{"fake", "by-tool", "--pattern=a b", "--tag", "x_y-z.1:2"},
	}
	for _, a := range ok {
		if err := CheckArgs(a); err != nil {
			t.Errorf("%v refused: %v", a, err)
		}
	}
	bad := [][]string{
		{"fake;id"},
		{"fake", "a|b"},
		{"fake", "$(id)"},
		{"fake", "`id`"},
		{"fake", "a\nb"},
		{"fake", "a&b"},
		{"fake", "a>b"},
		{"fake", "--scope", "x"},
		{"fake", "--from=1"},
		{"fake", "--to", "1"},
		{"fake", "--until", "1"},
		{"fake", "--query", "x"},
	}
	for _, a := range bad {
		err := CheckArgs(a)
		var re *RefusedError
		if !errors.As(err, &re) {
			t.Errorf("%v: err %v, want a refusal", a, err)
		}
	}
}

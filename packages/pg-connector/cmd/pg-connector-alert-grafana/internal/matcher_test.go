package internal

import (
	"reflect"
	"testing"
)

func TestTranslateMatcherSet(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{name: "empty string is whole set", in: "", want: nil},
		{name: "empty braces is whole set", in: "{}", want: nil},
		{name: "equals", in: `{severity="critical"}`, want: []string{`severity="critical"`}},
		{name: "not equals", in: `{severity!="info"}`, want: []string{`severity!="info"`}},
		{name: "regex match", in: `{severity=~"critical|warning"}`, want: []string{`severity=~"critical|warning"`}},
		{name: "regex not match", in: `{alertname!~"Test.*"}`, want: []string{`alertname!~"Test.*"`}},
		{
			name: "comma-joined ANDed become repeated filters",
			in:   `{severity="critical", alertname=~"Disk.*"}`,
			want: []string{`severity="critical"`, `alertname=~"Disk.*"`},
		},
		{name: "braces optional", in: `severity="critical"`, want: []string{`severity="critical"`}},
		{name: "unquoted value is quoted", in: `{severity=critical}`, want: []string{`severity="critical"`}},
		{name: "comma inside quoted value is not a separator", in: `{a=~"x,y", b="z"}`, want: []string{`a=~"x,y"`, `b="z"`}},
		{name: "escaped quote round trips", in: `{a="x\"y"}`, want: []string{`a="x\"y"`}},
		{name: "trailing comma tolerated", in: `{a="b",}`, want: []string{`a="b"`}},
		{name: "unbalanced open brace", in: `{a="b"`, wantErr: true},
		{name: "unbalanced close brace", in: `a="b"}`, wantErr: true},
		{name: "no operator", in: `{severity}`, wantErr: true},
		{name: "no name", in: `{="x"}`, wantErr: true},
		{name: "unterminated quote", in: `{a="x}`, wantErr: true},
		{name: "stray quote in unquoted value", in: `{a=x"y}`, wantErr: true},
		{name: "bad operator", in: `{a~"x"}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := translateMatcherSet(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

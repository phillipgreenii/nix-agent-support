package event_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/event"
)

func TestValidText(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want error
	}{
		{"no strings", nil, nil},
		{"empty string", []string{""}, nil},
		{"ascii", []string{"hello"}, nil},
		{"multi-byte text", []string{"café 日本語 \U0001f600"}, nil},
		{"newline, tab and U+2028", []string{"a\nb\tc d"}, nil},
		{"the byte 0xff", []string{"ok", "bad\xff"}, event.ErrInvalidUTF8},
		{"a truncated multi-byte sequence", []string{"\xe6\x97"}, event.ErrInvalidUTF8},
		{"an encoded surrogate", []string{"\xed\xa0\x80"}, event.ErrInvalidUTF8},
		{"exactly the event limit", []string{strings.Repeat("a", event.MaxEventBytes)}, nil},
		{"one byte over the event limit", []string{strings.Repeat("a", event.MaxEventBytes+1)}, event.ErrTooLarge},
		{"300 KiB", []string{strings.Repeat("a", 300*1024)}, event.ErrTooLarge},
		{"several strings that together are too long", []string{strings.Repeat("a", event.MaxEventBytes/2+1), strings.Repeat("b", event.MaxEventBytes/2+1)}, event.ErrTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := event.ValidText(c.in...)
			if !errors.Is(err, c.want) || (c.want == nil) != (err == nil) {
				t.Errorf("ValidText = %v, want %v", err, c.want)
			}
		})
	}
}

func TestValidReason(t *testing.T) {
	for _, blank := range []string{
		"", "  ", "\t\n", " \t \n ", " ", "　", "  　", " ", "\r\n",
	} {
		if got, err := event.ValidReason(blank); err == nil {
			t.Errorf("ValidReason(%q) = %q, want an error", blank, got)
		}
	}
	for in, want := range map[string]string{
		" late ":              "late",
		"\tlate\n":            "late",
		" late　":              "late",
		"two  words inside":   "two  words inside",
		"no surrounding trim": "no surrounding trim",
	} {
		got, err := event.ValidReason(in)
		if err != nil || got != want {
			t.Errorf("ValidReason(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := event.ValidReason("bad\xff"); !errors.Is(err, event.ErrInvalidUTF8) {
		t.Errorf("ValidReason(0xff) = %v, want ErrInvalidUTF8", err)
	}
	if _, err := event.ValidReason(strings.Repeat("a", event.MaxEventBytes+1)); !errors.Is(err, event.ErrTooLarge) {
		t.Errorf("ValidReason(too long) = %v, want ErrTooLarge", err)
	}
}

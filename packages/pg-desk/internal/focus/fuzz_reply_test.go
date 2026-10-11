package focus

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// FuzzParseReply: ParseReply never panics, an error comes with a zero Reply
// and is a usage error, and a success carries at least one token's worth of
// meaning and no key on both sides.
//
// Traces INV-FOCUS-19: the reply parser never panics and rejects malformed replies.
func FuzzParseReply(f *testing.F) {
	for _, s := range []string{
		"", "ok", "OK -a +b cap=3", "cap=0", "cap=-1", "cap=", "-X +X", "ok\r\n-a",
		"+", "-", ",,,", "cap=2 cap=3", "\x00", "ok\xff", "+é -é", " ok", "cap=١٢",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got, err := ParseReply(strings.NewReader(in))
		if err != nil {
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("error %v is not ErrUsage", err)
			}
			if !reflect.DeepEqual(got, Reply{}) {
				t.Fatalf("error with output %+v", got)
			}
			return
		}
		if !got.Ok && len(got.Plus) == 0 && len(got.Minus) == 0 && got.Cap == nil {
			t.Fatalf("success with no content for %q", in)
		}
		if got.Cap != nil && *got.Cap < 1 {
			t.Fatalf("cap %d is not positive", *got.Cap)
		}
		minus := map[string]bool{}
		for _, k := range got.Minus {
			if k == "" {
				t.Fatalf("empty key in Minus")
			}
			minus[k] = true
		}
		for _, k := range got.Plus {
			if k == "" || minus[k] {
				t.Fatalf("bad Plus key %q", k)
			}
		}
	})
}

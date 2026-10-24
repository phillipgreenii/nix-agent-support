package prchange

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-decider/internal/item"
)

func TestParseSeq(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		ok      bool
		wantErr bool
	}{
		{"17", 17, true, false},
		{" 17 ", 17, true, false},
		{"10000000", 10000000, true, false},
		// A sequence of 1,000,000 or more renders in exponent form.
		{"1.234567e+06", 1234567, true, false},
		{"1e+07", 10000000, true, false}, // 10^7
		{"1E7", 10000000, true, false},
		{"9.007199254740992e+15", 9007199254740992, true, false},
		// Absent: nothing, what a template renders for a missing key, a null.
		{"", 0, false, false},
		{"<no value>", 0, false, false},
		{"<NO VALUE>", 0, false, false},
		{"<nil>", 0, false, false},
		{"null", 0, false, false},
		// 0 is never a key.
		{"0", 0, false, false},
		{"0.0", 0, false, false},
		{"0e+00", 0, false, false},
		// Not a sequence: refused, never read as absent.
		{"abc", 0, false, true},
		{"1.5", 0, false, true},
		{"-3", 0, false, true},
		{"-1e+07", 0, false, true},
		{"1e400", 0, false, true},
		{"1e19", 0, false, true}, // beyond int64
		{"NaN", 0, false, true},
		{"Inf", 0, false, true},
		{"1/2", 0, false, true},
	}
	for _, c := range cases {
		got, ok, err := ParseSeq(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseSeq(%q): err = %v, want error %v", c.in, err, c.wantErr)
			continue
		}
		if err == nil && (got != c.want || ok != c.ok) {
			t.Errorf("ParseSeq(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestResolveKeyOrder(t *testing.T) {
	cases := []struct {
		name, seq, id string
		want          DeliveryKey
	}{
		{"the change sequence wins over a suffix", "5", "acme/api#7@0123456789ab", DeliveryKey{5, KeySourceSequence}},
		{"no sequence takes the suffix value", "", "acme/api#7@0123456789ab", DeliveryKey{0x0123456789ab, KeySourceSuffix}},
		{"a missing template key takes the suffix value", "<no value>", "acme/api#7@ffffffffffff", DeliveryKey{0xffffffffffff, KeySourceSuffix}},
		{"upper-case hex digits", "", "acme/api#7@ABCDEF012345", DeliveryKey{0xabcdef012345, KeySourceSuffix}},
		{"a bare id and no sequence has no key", "", "acme/api#7", DeliveryKey{0, KeySourceNone}},
		{"a sequence of 0 is no sequence", "0", "acme/api#7", DeliveryKey{0, KeySourceNone}},
		{"a sequence of 0 falls to the suffix", "0", "acme/api#7@000000000abc", DeliveryKey{0xabc, KeySourceSuffix}},
		{"a suffix of all zeros is no key", "", "acme/api#7@000000000000", DeliveryKey{0, KeySourceNone}},
		{"a suffix that is not 12 hex digits is no key", "", "acme/api#7@abc", DeliveryKey{0, KeySourceNone}},
		{"exponent form is parsed", "1.234567e+06", "acme/api#7", DeliveryKey{1234567, KeySourceSequence}},
	}
	for _, c := range cases {
		got, err := ResolveKey(c.seq, c.id)
		if err != nil || got != c.want {
			t.Errorf("%s: ResolveKey(%q, %q) = %+v, %v; want %+v", c.name, c.seq, c.id, got, err, c.want)
		}
		if got.Has() != (c.want.Source != KeySourceNone) {
			t.Errorf("%s: Has() = %v", c.name, got.Has())
		}
		if got.Source != KeySourceNone && got.Value == 0 {
			t.Errorf("%s: a key of 0 would be passed", c.name)
		}
	}
	if _, err := ResolveKey("garbage", "acme/api#7"); err == nil {
		t.Error("a sequence that is not a number was accepted")
	}
}

// The item the command builds is the item the decider's item.Routed reads: its
// identity and delivery key, and nothing else the decider would decide from.
func TestRoutedItemIsWhatTheDeciderReads(t *testing.T) {
	out := RoutedItemJSON(" acme/api#7@0123456789ab ", "acme/api#7", DeliveryKey{0x0123456789ab, KeySourceSuffix})
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("the item is not one line: %q", out)
	}
	var it item.Routed
	if err := json.Unmarshal([]byte(out), &it); err != nil {
		t.Fatal(err)
	}
	if it.ID != "acme/api#7@0123456789ab" || it.Type != "pr.changed" || it.Title == "" ||
		it.Metadata.EntityType != "pr" || it.Metadata.EntityID != "acme/api#7" || it.Metadata.Kind != "changed" ||
		it.Metadata.Seq != 0x0123456789ab {
		t.Fatalf("item = %+v", it)
	}
}

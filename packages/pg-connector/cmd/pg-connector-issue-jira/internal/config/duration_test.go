package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDurationSyntax(t *testing.T) {
	for in, want := range map[string]time.Duration{
		`"60s"`: time.Minute, `"1m"`: time.Minute, `"24h"`: 24 * time.Hour,
		`"7d"`: 7 * 24 * time.Hour, `"1d12h"`: 36 * time.Hour, `"500ms"`: 500 * time.Millisecond,
	} {
		var d Duration
		if err := json.Unmarshal([]byte(in), &d); err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if d.Std() != want {
			t.Errorf("%s = %v, want %v", in, d.Std(), want)
		}
	}
	for _, in := range []string{`60`, `"soon"`, `""`, `true`, `"xd"`} {
		var d Duration
		if err := json.Unmarshal([]byte(in), &d); err == nil {
			t.Errorf("%s: expected an error", in)
		}
	}
}

func TestDurationNullKeepsValueAndMarshalsCanonical(t *testing.T) {
	d := Duration(5 * time.Second)
	if err := json.Unmarshal([]byte(`null`), &d); err != nil || d.Std() != 5*time.Second {
		t.Fatalf("null changed the value: %v %v", d, err)
	}
	b, err := json.Marshal(Duration(time.Minute))
	if err != nil || string(b) != `"1m0s"` {
		t.Fatalf("marshal = %s %v", b, err)
	}
}

func TestByteSizeSyntax(t *testing.T) {
	for in, want := range map[string]ByteSize{
		`4096`: 4096, `"4096"`: 4096, `"4KiB"`: 4096, `"1MiB"`: 1 << 20, `"2GiB"`: 2 << 30, `"7B"`: 7,
	} {
		var b ByteSize
		if err := json.Unmarshal([]byte(in), &b); err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if b != want {
			t.Errorf("%s = %d, want %d", in, b, want)
		}
	}
	for _, in := range []string{`"big"`, `true`, `[]`} {
		var b ByteSize
		if err := json.Unmarshal([]byte(in), &b); err == nil {
			t.Errorf("%s: expected an error", in)
		}
	}
	b := ByteSize(9)
	if err := json.Unmarshal([]byte(`null`), &b); err != nil || b != 9 {
		t.Errorf("null changed the value: %d %v", b, err)
	}
	out, _ := json.Marshal(ByteSize(4096))
	if string(out) != `4096` {
		t.Errorf("marshal = %s", out)
	}
}

package connectord

import (
	"reflect"
	"testing"
)

func TestLoggerFuncLogHandsNameAndFieldsUnchanged(t *testing.T) {
	cases := []struct {
		name   string
		change string
		kv     []any
	}{
		{"no fields", "daemon_started", nil},
		{"even fields", "peer_uid_rejected", []any{"peer_uid", 501, "daemon_uid", 502}},
		{"odd fields keep the trailing key", "peer_uid_rejected", []any{"peer_uid", 501, "dangling"}},
		{"single key without a value", "x", []any{"only_key"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				calls    int
				gotName  string
				gotKV    []any
				gotIsNil bool
			)
			var l Logger = LoggerFunc(func(change string, kv ...any) {
				calls++
				gotName = change
				gotKV = kv
				gotIsNil = kv == nil
			})

			l.Log(tc.change, tc.kv...)

			if calls != 1 {
				t.Fatalf("function called %d times, want exactly 1", calls)
			}
			if gotName != tc.change {
				t.Errorf("change name = %q, want %q", gotName, tc.change)
			}
			if len(gotKV) != len(tc.kv) {
				t.Fatalf("field count = %d, want %d (%v)", len(gotKV), len(tc.kv), gotKV)
			}
			if len(tc.kv) > 0 && !reflect.DeepEqual(gotKV, tc.kv) {
				t.Errorf("fields = %#v, want %#v", gotKV, tc.kv)
			}
			if tc.kv == nil && !gotIsNil {
				t.Errorf("no-field call delivered a non-nil slice %#v", gotKV)
			}
		})
	}
}

func TestLoggerFuncLogCallsThroughMethodOnFuncValue(t *testing.T) {
	var got string
	f := LoggerFunc(func(change string, _ ...any) { got = change })
	f.Log("direct_call")
	if got != "direct_call" {
		t.Errorf("change name = %q, want direct_call", got)
	}
}

func TestNopLoggerAcceptsCalls(t *testing.T) {
	if NopLogger == nil {
		t.Fatal("NopLogger is nil")
	}
	// None of these may panic, including an odd count of fields.
	NopLogger.Log("anything")
	NopLogger.Log("with_fields", "k", 1, "k2", "v2")
	NopLogger.Log("odd_fields", "k", 1, "dangling")
}

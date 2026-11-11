package cutover

import "testing"

func TestEnabled(t *testing.T) {
	for _, tc := range []struct {
		val  string
		set  bool
		want bool
	}{
		{set: false, want: false},
		{val: "", set: true, want: false},
		{val: "0", set: true, want: false},
		{val: "true", set: true, want: false},
		{val: "yes", set: true, want: false},
		{val: "1", set: true, want: true},
	} {
		if tc.set {
			t.Setenv(EnvVar, tc.val)
		} else {
			t.Setenv(EnvVar, "")
		}
		if got := Enabled(); got != tc.want {
			t.Errorf("%s=%q (set=%v): Enabled() = %v, want %v", EnvVar, tc.val, tc.set, got, tc.want)
		}
	}
}

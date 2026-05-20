package output

import "testing"

func TestResolve(t *testing.T) {
	cases := []struct {
		name string
		flag bool
		env  string
		want bool
	}{
		{name: "flag wins over unset env", flag: true, env: "", want: true},
		{name: "flag wins over json env", flag: true, env: "json", want: true},
		{name: "flag wins over other env", flag: true, env: "yaml", want: true},
		{name: "env json with flag off", flag: false, env: "json", want: true},
		{name: "env empty with flag off", flag: false, env: "", want: false},
		{name: "env other with flag off", flag: false, env: "yaml", want: false},
		// env value is case-sensitive on purpose: matches docs/skill expectations.
		{name: "env uppercase JSON does not trigger", flag: false, env: "JSON", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvVar, tc.env)
			if got := Resolve(tc.flag); got != tc.want {
				t.Fatalf("Resolve(flag=%v, env=%q) = %v, want %v",
					tc.flag, tc.env, got, tc.want)
			}
		})
	}
}

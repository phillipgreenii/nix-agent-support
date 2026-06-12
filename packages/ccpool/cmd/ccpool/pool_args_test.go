package main

import (
	"os"
	"reflect"
	"testing"
)

func TestStripPoolFlag(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		wantArgv []string
		wantPool string
		wantErr  bool
	}{
		{"none", []string{"ccpool", "list"}, []string{"ccpool", "list"}, "", false},
		{"before cmd", []string{"ccpool", "--pool", "/p", "new", "a"}, []string{"ccpool", "new", "a"}, "/p", false},
		{"equals form", []string{"ccpool", "--pool=/p", "new"}, []string{"ccpool", "new"}, "/p", false},
		{"after cmd rejected", []string{"ccpool", "new", "--pool", "/p"}, nil, "", true},
		{"missing value", []string{"ccpool", "--pool"}, nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgv, gotPool, err := stripPoolFlag(tc.argv)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(gotArgv, tc.wantArgv) || gotPool != tc.wantPool {
				t.Errorf("got (%v,%q), want (%v,%q)", gotArgv, gotPool, tc.wantArgv, tc.wantPool)
			}
		})
	}
	_ = os.Args
}

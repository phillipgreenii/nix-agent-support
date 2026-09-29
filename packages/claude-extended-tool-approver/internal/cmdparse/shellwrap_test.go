package cmdparse

import (
	"reflect"
	"testing"
)

// TestUnwrapShellDashCChain_Unwraps covers the chain-unwrap loop's own
// contract (pg2-zsv1c): a leaf that is bash/sh -c shaped reparses its
// script into leaves, and a CHAIN of nested wrappers (`bash -c 'bash -c
// "..."'`) is fully resolved, mirroring nix.go's own innerCommandStructure
// loop exactly.
func TestUnwrapShellDashCChain_Unwraps(t *testing.T) {
	tests := []struct {
		name       string
		cmd        string
		wantOK     bool
		wantSource string
		wantLeaves int
	}{
		{
			name:       "single bash -c, single inner leaf",
			cmd:        `bash -c 'echo hi'`,
			wantOK:     true,
			wantSource: "echo hi",
			wantLeaves: 1,
		},
		{
			name:       "single sh -c, multi-statement script splits into multiple leaves",
			cmd:        `sh -c 'T=$(mktemp -d); HOME="$T"'`,
			wantOK:     true,
			wantSource: `T=$(mktemp -d); HOME="$T"`,
			wantLeaves: 2,
		},
		{
			name:       "chained bash -c 'bash -c ...' unwraps both layers",
			cmd:        `bash -c 'bash -c "echo inner"'`,
			wantOK:     true,
			wantSource: "echo inner",
			wantLeaves: 1,
		},
		{
			name:   "not bash/sh at all",
			cmd:    `echo hi`,
			wantOK: false,
		},
		{
			name:   "bash without -c",
			cmd:    `bash script.sh`,
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			leaves := Parse(tc.cmd)
			if len(leaves) != 1 {
				t.Fatalf("test setup: %q parsed to %d leaves, want 1", tc.cmd, len(leaves))
			}
			gotLeaves, gotSource, ok := UnwrapShellDashCChain(leaves[0])
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if gotSource != tc.wantSource {
				t.Errorf("source = %q, want %q", gotSource, tc.wantSource)
			}
			if len(gotLeaves) != tc.wantLeaves {
				t.Errorf("len(leaves) = %d, want %d", len(gotLeaves), tc.wantLeaves)
			}
		})
	}
}

// TestNestedShellDashCVars_PrefixAssignment covers the ONE channel by
// which an outer assignment reaches a nested bash -c payload's own
// environment: a PREFIX assignment on the bash/sh -c leaf itself, never a
// plain (non-prefix) assignment earlier in the enclosing expression.
func TestNestedShellDashCVars_PrefixAssignment(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want map[string]string
	}{
		{
			name: "literal prefix assignment is visible",
			cmd:  `T=/abs/dir bash -c 'echo $T'`,
			want: map[string]string{"T": "/abs/dir"},
		},
		{
			name: "command substitution prefix is not literal",
			cmd:  `T=$(mktemp -d) bash -c 'echo $T'`,
			want: nil,
		},
		{
			name: "no prefix assignment at all",
			cmd:  `bash -c 'echo hi'`,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			leaves := Parse(tc.cmd)
			if len(leaves) != 1 {
				t.Fatalf("test setup: %q parsed to %d leaves, want 1", tc.cmd, len(leaves))
			}
			got := NestedShellDashCVars(leaves[0])
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("NestedShellDashCVars() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestNestedShellDashCTempDirVars_PrefixAssignment is
// TestNestedShellDashCVars_PrefixAssignment's sibling for the fresh-temp-dir
// MARKER channel rather than a literal value.
func TestNestedShellDashCTempDirVars_PrefixAssignment(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want map[string]string
	}{
		{
			name: "mktemp -d prefix assignment is marked",
			cmd:  `T=$(mktemp -d) bash -c 'echo $T'`,
			want: map[string]string{"T": ""},
		},
		{
			name: "literal prefix assignment is not a tempdir marker",
			cmd:  `T=/abs/dir bash -c 'echo $T'`,
			want: nil,
		},
		{
			name: "no prefix assignment at all",
			cmd:  `bash -c 'echo hi'`,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			leaves := Parse(tc.cmd)
			if len(leaves) != 1 {
				t.Fatalf("test setup: %q parsed to %d leaves, want 1", tc.cmd, len(leaves))
			}
			got := NestedShellDashCTempDirVars(leaves[0])
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("NestedShellDashCTempDirVars() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestExactKeyDecoding pins item g's second acceptance bar: DecodeBashToolInput
// uses EXACT-KEY matching over a Bash tool_input payload — an unrecognised key
// is a decode failure, not a silently-dropped field, and a payload missing the
// required "command" key is ALSO a decode failure, not a zero-value empty
// command a loose json.Unmarshal would hand back.
func TestExactKeyDecoding(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "well-formed payload decodes the command",
			raw:  `{"command":"echo hi"}`,
			want: "echo hi",
		},
		{
			name: "empty-string command is a valid, present value",
			raw:  `{"command":""}`,
			want: "",
		},
		{
			name:    "an unrecognized extra key is a decode failure, not silently ignored",
			raw:     `{"command":"echo hi","sandbox":true}`,
			wantErr: true,
		},
		{
			name:    "an unrecognized key alone, with no command at all, is a decode failure",
			raw:     `{"description":"list files"}`,
			wantErr: true,
		},
		{
			name:    "missing command key is a decode failure, not the zero-value empty string",
			raw:     `{}`,
			wantErr: true,
		},
		{
			name:    "malformed JSON is a decode failure",
			raw:     `{"command":`,
			wantErr: true,
		},
		{
			name:    "a JSON array instead of an object is a decode failure",
			raw:     `["echo hi"]`,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeBashToolInput([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("DecodeBashToolInput(%q) = %q, nil; want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeBashToolInput(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("DecodeBashToolInput(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

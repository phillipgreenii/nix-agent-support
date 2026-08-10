package cmdparse

import "testing"

// one returns the single leaf of cmd, failing if it does not parse to exactly one.
func one(t *testing.T, cmd string) ParsedCommand {
	t.Helper()
	leaves := Parse(cmd)
	if len(leaves) != 1 {
		t.Fatalf("Parse(%q) = %d leaves, want 1", cmd, len(leaves))
	}
	return leaves[0]
}

// TestStageWritesInput covers the sink classification relocated here from the
// gitdir rule (tc-yk2z): an ALLOWLIST of consume-without-persist stages, with an
// unknown stage treated as a writer.
func TestStageWritesInput(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		// Known filters consume without persisting.
		{"grep url", false},
		{"head -20", false},
		{"jq -r .x", false},
		{"sed 's/a/b/'", false},
		{"awk '{print $1}'", false},
		{"wc -l", false},
		{"base64", false},
		// The shape the allowlist exists to catch.
		{"tee /tmp/x", true},
		{"tee -a /tmp/x", true},
		// UNKNOWN IS A WRITER. None of these was ever on a denylist, which is the
		// whole argument for the allowlist direction.
		{"dd of=/tmp/x", true},
		{"sponge /tmp/x", true},
		{"split -l 100", true},
		{"logger -t x", true},
		{"xargs rm", true},
		{"sh -c 'cat > /tmp/x'", true},
		// A filter carrying a flag that makes it write a file is a writer after all.
		{"sort -o /tmp/x", true},
		{"sort --output=/tmp/x", true},
		{"yq -i .a", true},
		{"sort -u", false},
		// A capturing stdout redirection persists the payload whatever the stage is.
		{"grep url > /tmp/x", true},
		{"grep url >> /tmp/x", true},
		{"grep url > /dev/null", false},
		{"grep url 2>/dev/null", false},
		// A leading shell keyword must be stepped past, not read as the command.
		{"! grep url", false},
		{"time tee /tmp/x", true},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := StageWritesInput(one(t, tt.cmd)); got != tt.want {
				t.Errorf("StageWritesInput(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

// TestStageWritesInputWith proves the per-caller filter SET is a real seam: the
// mechanism is shared, the vocabulary need not be. An empty set makes everything a
// writer, which is the fail-closed direction.
func TestStageWritesInputWith(t *testing.T) {
	if !StageWritesInputWith(map[string]bool{}, one(t, "grep url")) {
		t.Error("empty filter set: grep should be a writer (fail closed)")
	}
	if StageWritesInputWith(map[string]bool{"tee": true}, one(t, "tee /tmp/x")) {
		t.Error("caller set naming tee a filter: tee should not be a writer")
	}
}

// TestPipedToWriter pins the packaged DownstreamStages+StageWritesInput loop, and
// in particular that DIRECTION matters: a writer UPSTREAM of the leaf is not the
// leaf's sink.
func TestPipedToWriter(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		leafRaw string
		want    bool
	}{
		{"no pipeline", "cat f", "cat f", false},
		{"filter sink", "cat f | grep url", "cat f", false},
		{"writing sink", "cat f | tee /tmp/x", "cat f", true},
		{"unknown sink", "cat f | dd of=/tmp/x", "cat f", true},
		{"far sink", "cat f | grep url | head -1 | tee /tmp/x", "cat f", true},
		{"sink is last stage", "cat f | tee /tmp/x", "tee /tmp/x", false},
		{"upstream writer is not my sink", "tee /tmp/x | grep url", "grep url", false},
		{"capturing filter", "cat f | grep url > /tmp/x", "cat f", true},
		{"different pipeline", "cat f ; tee /tmp/x", "cat f", false},
		{"&& is not a pipe", "cat f && tee /tmp/x", "cat f", false},
		{"unmatched leaf", "cat f | tee /tmp/x", "no such leaf", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PipedToWriter(Parse(tt.expr), tt.leafRaw); got != tt.want {
				t.Errorf("PipedToWriter(%q, %q) = %v, want %v", tt.expr, tt.leafRaw, got, tt.want)
			}
		})
	}
}

func TestEffectiveExec(t *testing.T) {
	tests := []struct {
		cmd      string
		wantBase string
		wantArgs int
	}{
		{"ls -la", "ls", 1},
		{"/usr/bin/ls -la", "ls", 1},
		{"if [ -e x ]", "[", 3},
		{"time tee /tmp/x", "tee", 1},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			base, args := EffectiveExec(one(t, tt.cmd))
			if base != tt.wantBase || len(args) != tt.wantArgs {
				t.Errorf("EffectiveExec(%q) = (%q, %v), want base %q with %d args",
					tt.cmd, base, args, tt.wantBase, tt.wantArgs)
			}
		})
	}
}

func TestHasAnyFlag(t *testing.T) {
	flags := map[string]bool{"-o": true, "--output": true}
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"separate short", []string{"-o", "f"}, true},
		{"glued short", []string{"-o=f"}, true},
		{"glued long", []string{"--output=f"}, true},
		{"absent", []string{"-u", "-n"}, false},
		{"not a prefix match", []string{"-output"}, false},
		{"empty flag set", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := flags
			if tt.name == "empty flag set" {
				set = nil
			}
			if got := HasAnyFlag(tt.args, set); got != tt.want {
				t.Errorf("HasAnyFlag(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestCapturesStdout(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"cat f", false},
		{"cat f > /tmp/x", true},
		{"cat f >> /tmp/x", true},
		{"cat f > /dev/null", false},
		{"cat f > /dev/stdout", false},
		{"cat f 2>/tmp/err", false}, // stderr only — captures none of the payload
		{"cat f 2>&1", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := CapturesStdout(one(t, tt.cmd)); got != tt.want {
				t.Errorf("CapturesStdout(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

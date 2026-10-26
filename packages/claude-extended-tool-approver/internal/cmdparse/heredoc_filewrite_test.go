package cmdparse

import "testing"

// TestHeredocFileWriteCleared pins the static shape test behind the engine's
// "cat > FILE <<'EOF'" carve-out (operator ruling 2026-10-25). It decides ONLY the
// shape of the leaf; whether the target FILE may be written is the engine's
// redirection check, covered end to end by the engine and CLI integration suites.
func TestHeredocFileWriteCleared(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		// --- admitted ---
		{"cat > FILE, quoted delimiter", "cat > /tmp/f <<'EOF'\nbody\nEOF", true},
		{"cat >> FILE, quoted delimiter", "cat >> /tmp/f <<'EOF'\nbody\nEOF", true},
		{"redirect after the heredoc operator", "cat <<'EOF' > /tmp/f\nbody\nEOF", true},
		{"/bin/cat spelling", "/bin/cat > /tmp/f <<'EOF'\nbody\nEOF", true},
		{"double-quoted delimiter", "cat > /tmp/f <<\"EOF\"\nbody\nEOF", true},
		{"<<- tab-stripping form", "cat > /tmp/f <<-'EOF'\n\tbody\n\tEOF", true},
		{"bare formatting flag", "cat -n > /tmp/f <<'EOF'\nbody\nEOF", true},
		{"stderr discarded alongside the file write", "cat > /tmp/f 2>/dev/null <<'EOF'\nbody\nEOF", true},
		{"quoted body may hold anything: it is literal", "cat > /tmp/f <<'EOF'\n$(rm -rf /) ${x@P} $((1+1))\nEOF", true},
		{"unquoted body, plain $NAME and ${NAME}", "cat > /tmp/f <<EOF\n$HOME ${HOME}\nEOF", true},
		{"unquoted body, command substitution (the engine recurses it)", "cat > /tmp/f <<EOF\n$(echo hi)\nEOF", true},

		// --- refused: the leaf is not a file write of a heredoc ---
		{"plain cat heredoc to the terminal is not covered", "cat <<'EOF'\nbody\nEOF", false},
		{"stderr-only redirect captures no stdout", "cat 2> /tmp/e <<'EOF'\nbody\nEOF", false},
		{"stdout to /dev/null captures nothing", "cat > /dev/null <<'EOF'\nbody\nEOF", false},
		{"no heredoc at all", "cat > /tmp/f", false},
		{"interpreter reader", "sh > /tmp/f <<'EOF'\nbody\nEOF", false},
		{"python reader", "python > /tmp/f <<'EOF'\nbody\nEOF", false},
		{"tee is not on the reader allowlist", "tee /tmp/f <<'EOF'\nbody\nEOF", false},

		// --- refused: cat could emit something other than the heredoc body ---
		{"positional file operand replaces the body", "cat /etc/hosts > /tmp/f <<'EOF'\nbody\nEOF", false},
		{"explicit stdin redirect competes with the heredoc", "cat < /etc/hosts > /tmp/f <<'EOF'\nbody\nEOF", false},
		{"herestring beside the heredoc", "cat > /tmp/f <<< hi <<'EOF'\nbody\nEOF", false},
		{"herestring alone", "cat > /tmp/f <<< hi", false},

		// --- refused: an unquoted body expansion the engine does not recurse ---
		{"arithmetic expansion evaluates variable text", "cat > /tmp/f <<EOF\n$((X))\nEOF", false},
		{"legacy arithmetic $[ ]", "cat > /tmp/f <<EOF\n$[X]\nEOF", false},
		{"prompt transform evaluates substitutions in the value", "cat > /tmp/f <<EOF\n${x@P}\nEOF", false},
		{"default-value form", "cat > /tmp/f <<EOF\n${x:-y}\nEOF", false},
		{"indexed form", "cat > /tmp/f <<EOF\n${x[1]}\nEOF", false},
		{"one unquoted extent among quoted ones refuses the whole leaf", "cat > /tmp/f <<'A' <<B\nbody\nA\n${x@P}\nB", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaves := Parse(tt.command)
			if len(leaves) != 1 {
				t.Fatalf("Parse(%q) = %d leaves, want exactly 1 (fixture is meant to be a single leaf)", tt.command, len(leaves))
			}
			if got := HeredocFileWriteCleared(leaves[0]); got != tt.want {
				t.Errorf("HeredocFileWriteCleared(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

package subshell

import "testing"

func TestCountParsesPgrepOutput(t *testing.T) {
	c := &Counter{
		RunPs: func(parent int) (string, error) {
			if parent != 42 {
				t.Fatalf("unexpected parent %d", parent)
			}
			return "100 zsh\n101 bash\n102 ssh-agent\n103 /bin/sh\n", nil
		},
	}
	n, err := c.Count(42)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("Count = %d, want 3 (zsh+bash+sh)", n)
	}
}

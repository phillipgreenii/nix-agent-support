package originguard

import (
	"context"
	"os/exec"
)

func execCommand(name string, args ...string) *exec.Cmd {
	return exec.CommandContext(context.Background(), name, args...)
}

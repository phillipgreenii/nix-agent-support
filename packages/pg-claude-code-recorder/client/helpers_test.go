package client_test

import (
	"errors"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/client"
)

func tempDir() (string, error) { return os.MkdirTemp("/tmp", "ccrec") }
func removeAll(d string)       { _ = os.RemoveAll(d) }

func errorsIs(err, target error) bool           { return errors.Is(err, target) }
func errorsAs(err error, t **client.Error) bool { return errors.As(err, t) }

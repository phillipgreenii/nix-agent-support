package client_test

import (
	"os"
)

func tempDir() (string, error) { return os.MkdirTemp("/tmp", "ccrec") }
func removeAll(d string)       { _ = os.RemoveAll(d) }

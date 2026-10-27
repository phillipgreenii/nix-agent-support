package main

import (
	"os"
	"sync"
)

// The client and status modes are tested as the real binary: built with go
// build (the budget seam linked in, so a daemon-down read does not wait out the
// production budget), run as a child with an otherwise empty environment and
// working directory "/", against an in-process daemon on a short socket (the
// real process shell, socket server and router), the way the umbrella execs it.

var (
	buildOnce sync.Once
	buildDir  string
	buildBin  string
	buildErr  error
)

// removeBuilds deletes the built binary; TestMain calls it after the run.
func removeBuilds() {
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
}

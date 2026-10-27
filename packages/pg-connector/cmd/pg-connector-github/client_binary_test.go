package main

import (
	"os"
	"sync"
)

// The client is tested as the real binary: built with go build, run as a child
// with an empty environment and working directory "/", against an in-process
// daemon on a short temp socket (the real process shell, the real socket
// server, the real router), the way the umbrella execs it.

var (
	buildMu   sync.Mutex
	buildRoot string
	buildDirs = map[string]string{}
)

// removeBuilds deletes the built binaries; TestMain calls it after the run.
func removeBuilds() {
	buildMu.Lock()
	defer buildMu.Unlock()
	if buildRoot != "" {
		_ = os.RemoveAll(buildRoot)
	}
}

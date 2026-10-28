// Command orphanwatch is the helper of the internal/orphan real-orphan test. It
// is built once per test run WITHOUT the race detector (testenv.BuildHelper),
// so neither role pays the race runtime's 1.0 s exit sleep.
//
// Two roles, chosen by GO_ORPHAN_ROLE. "parent" starts a "child" that runs the
// real orphan.WatchWith, waits until the child is watching, then exits,
// orphaning it. The child must run its cleanup and go away. ORPHAN_TEST_DIR
// is where they publish what happened.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/phillipgreenii/pg-rescue/internal/orphan"
)

func main() {
	dir := os.Getenv("ORPHAN_TEST_DIR")
	switch os.Getenv("GO_ORPHAN_ROLE") {
	case "parent":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "GO_ORPHAN_ROLE=child")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(filepath.Join(dir, "watching")); err == nil {
				os.Exit(0) // orphan the child
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(3)
	case "child":
		time.AfterFunc(30*time.Second, func() { os.Exit(4) }) // never leak a stray process
		orphan.WatchWith(orphan.Options{
			Interval: 20 * time.Millisecond,
			Cleanup: func() {
				_ = os.WriteFile(filepath.Join(dir, "cleanup"), []byte("ran"), 0o600)
			},
		})
		_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		_ = os.WriteFile(filepath.Join(dir, "watching"), nil, 0o600)
		select {}
	}
	os.Exit(99)
}

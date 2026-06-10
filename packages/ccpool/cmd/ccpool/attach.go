package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/phillipgreenii/ccpool/internal/config"
)

func runAttach(args []string) int {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool attach <name>")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	target := cfg.Tmux.Prefix + fs.Arg(0)
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tmux not found")
		return 1
	}
	// Replace this process with the interactive attach.
	argv := []string{"tmux", "-L", cfg.Tmux.Socket, "attach", "-t", target}
	if err := syscall.Exec(tmuxBin, argv, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "attach:", err)
		return 1
	}
	return 0
}

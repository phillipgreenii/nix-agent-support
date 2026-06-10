package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

func runClose(args []string) int {
	fs := flag.NewFlagSet("close", flag.ExitOnError)
	purge := fs.Bool("purge", false, "also delete the store row (drops the name->uuid map)")
	// Re-order so flags can appear after the positional name arg (Unix convention).
	var flags, posArgs []string
	for _, a := range args {
		if len(a) > 0 && a[0] == '-' {
			flags = append(flags, a)
		} else {
			posArgs = append(posArgs, a)
		}
	}
	_ = fs.Parse(append(flags, posArgs...))
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool close <name> [--purge]")
		return 2
	}
	svc, st, code := buildService()
	if code != 0 {
		return code
	}
	defer st.Close()
	if err := svc.Close(context.Background(), fs.Arg(0), *purge); err != nil {
		fmt.Fprintln(os.Stderr, "close:", err)
		return 1
	}
	return 0
}

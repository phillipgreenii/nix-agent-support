package main

import (
	"context"
	"flag"
	"fmt"
	"os"
)

func runClose(args []string) int {
	fs := flag.NewFlagSet("close", flag.ExitOnError)
	purge := fs.Bool("purge", false, "also delete the store row (drops the external_id->claude_session_id map)")
	pos := parseInterspersed(fs, args) // flags may follow the positional external_id
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool close <external_id> [--purge]")
		return 2
	}
	svc, st, code := buildService()
	if code != 0 {
		return code
	}
	defer func() { _ = st.Close() }()
	if err := svc.Close(context.Background(), pos[0], *purge); err != nil {
		fmt.Fprintln(os.Stderr, "close:", err)
		return 1
	}
	return 0
}

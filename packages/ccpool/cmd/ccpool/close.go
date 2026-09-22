package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// closeReasonVocabulary renders store.CloseReasons as a stable, sorted
// "|"-joined list for CLI usage/error messages (map iteration order is not
// stable, so this must sort rather than range directly).
func closeReasonVocabulary() string {
	names := make([]string, 0, len(store.CloseReasons))
	for k := range store.CloseReasons {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

// validateCloseReason reports an error naming the vocabulary when reason is
// not one store.SetCloseReason accepts. Pure, so the CLI's rejection path is
// unit-testable without touching the store.
func validateCloseReason(reason string) error {
	if !store.CloseReasons[reason] {
		return fmt.Errorf("close: reason %q not one of %s", reason, closeReasonVocabulary())
	}
	return nil
}

// purgeReasonWarning returns the stderr warning line when --purge is combined
// with a non-default reason (closeWithReason skips the stamp on purge, so the
// reason is about to be silently discarded), or "" when no warning applies.
// Pure, so it is unit-testable without any I/O.
func purgeReasonWarning(purge bool, reason string) string {
	if purge && reason != "operator" {
		return "warning: --purge deletes the row; reason discarded"
	}
	return ""
}

func runClose(args []string) int {
	fs := flag.NewFlagSet("close", flag.ExitOnError)
	purge := fs.Bool("purge", false, "also delete the store row (drops the external_id->claude_session_id map)")
	reason := fs.String("reason", "operator", "why ccpool is closing this session ("+closeReasonVocabulary()+")")
	pos := parseInterspersed(fs, args) // flags may follow the positional external_id
	if len(pos) < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool close <external_id> [--purge] [-reason R]")
		return 2
	}
	if err := validateCloseReason(*reason); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if w := purgeReasonWarning(*purge, *reason); w != "" {
		fmt.Fprintln(os.Stderr, w)
	}
	svc, st, code := buildService()
	if code != 0 {
		return code
	}
	defer func() { _ = st.Close() }()
	if err := svc.CloseReason(context.Background(), pos[0], *reason, *purge); err != nil {
		slog.Error("close: failed", "err", err)
		return 1
	}
	if *purge {
		fmt.Printf("purged %s\n", pos[0])
	} else {
		fmt.Printf("closed %s (reason=%s)\n", pos[0], *reason)
	}
	return 0
}

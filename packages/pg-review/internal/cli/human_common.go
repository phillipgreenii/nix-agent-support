package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/exitcode"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/render"
)

// parseIDs reads positive integer item ids.
func parseIDs(args []string) ([]int64, error) {
	var ids []int64
	for _, a := range args {
		n, err := strconv.ParseInt(a, 10, 64)
		if err != nil || n <= 0 {
			return nil, usageErrorf("%q is not an item id (a positive integer)", a)
		}
		ids = append(ids, n)
	}
	return ids, nil
}

// mapHumanError maps ledger errors of the human commands to the exit table:
// an unknown id exits 4, a stale or missing view or a state conflict exits 6.
func mapHumanError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ledger.ErrNotFound):
		return ExitErrorf(exitcode.ValidationRejected, "%v", err)
	case errors.Is(err, ledger.ErrNotShown), errors.Is(err, ledger.ErrStaleView), errors.Is(err, ledger.ErrNotRulable),
		errors.Is(err, ledger.ErrIntegrity), errors.Is(err, ledger.ErrInvalid), errors.Is(err, ledger.ErrSchemaTooNew),
		errors.Is(err, ledger.ErrDuplicate):
		return ExitErrorf(exitcode.Ledger, "%v", err)
	}
	if s := err.Error(); strings.Contains(s, "database is locked") || strings.Contains(s, "SQLITE_BUSY") {
		return ExitErrorf(exitcode.Ledger, "ledger busy: %v", err)
	}
	return err
}

// shownText is the stored rendering with the real number, state and pending
// count, stripped of control characters.
func shownText(it ledger.Item, pending int) string {
	return render.Strip(render.Renumber(it.Rendered, int(it.Number), it.State, pending))
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(render.Strip(s)), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

func ageText(d float64) string {
	switch {
	case d >= 48*3600:
		return fmt.Sprintf("%dd", int(d/86400))
	case d >= 3600:
		return fmt.Sprintf("%dh", int(d/3600))
	}
	return fmt.Sprintf("%dm", int(d/60))
}

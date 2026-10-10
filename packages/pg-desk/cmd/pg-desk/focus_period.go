package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Period addressing for the focus verbs (spec section 7, "The addressed
// period (one rule)"). Every verb addresses ONE period: --date, else today
// in focus.time_zone. It is NEVER taken from a stored draft.

// focusPeriod is the one period a verb addresses.
type focusPeriod struct {
	Type, Key string
}

// resolveFocusPeriod returns the period the verb addresses and whether it is
// today in focus.time_zone. --date (any 2006-1-2 spelling) names the day,
// else the day containing now in the configured zone. --period must be
// "day", the only implemented value; any other value, and an unparseable
// --date, is a usage error (exit 1). A nil cfg addresses time.Local.
func resolveFocusPeriod(cmd *cobra.Command, cfg *config.Config, now time.Time) (focusPeriod, bool, error) {
	if pt, err := cmd.Flags().GetString(focusFlagPeriod); err == nil && pt != focusPeriodTypeDay {
		return focusPeriod{}, false, focusUsageError(
			fmt.Sprintf("--period %q is not implemented (only %q)", pt, focusPeriodTypeDay),
			"drop --period or pass --period day",
		)
	}
	loc := time.Local
	if cfg != nil {
		loc = cfg.FocusTimeZone()
	}
	todayKey := now.In(loc).Format(focusDayLayout)
	key := todayKey
	if date, err := cmd.Flags().GetString(focusFlagDate); err == nil && date != "" {
		norm, nerr := store.NormalizeFocusPeriodKey(focusPeriodTypeDay, date)
		if nerr != nil {
			return focusPeriod{}, false, focusUsageError(
				fmt.Sprintf("--date %q is not a date", date),
				"pass --date YYYY-MM-DD",
			)
		}
		key = norm
	}
	return focusPeriod{Type: focusPeriodTypeDay, Key: key}, key == todayKey, nil
}

// isApplicableDraft reports whether the stored draft d is for the addressed
// period p: the single term the spec uses for "a draft this verb may use".
func isApplicableDraft(d store.FocusDraft, p focusPeriod) bool {
	return d.PeriodType == p.Type && d.PeriodKey == p.Key
}

// isStaleDraft reports whether the stored draft d is stale: its period key is
// earlier than today (todayKey, a canonical day key) or its period is closed.
// periodClosed reports whether the period with a given key is closed (nil
// means none is). Keys are canonical 2006-01-02 strings, so the string order
// is the date order.
func isStaleDraft(d store.FocusDraft, todayKey string, periodClosed func(key string) bool) bool {
	if d.PeriodKey < todayKey {
		return true
	}
	return periodClosed != nil && periodClosed(d.PeriodKey)
}

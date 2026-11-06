package config

// The calendar trigger's configuration layer: the key checks, the error texts
// and the mapping onto query.CalendarTrigger. The schedule arithmetic itself is
// in internal/query/calendar.go.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	// The nix build sandbox has no host zone files, so zone-name validation
	// would fail there without the embedded copy. Host zone files win when
	// present, so tests pin only stable transitions.
	_ "time/tzdata"

	"github.com/phillipgreenii/pg-router/internal/query"
)

// hostZone is the zone of the router host, read once at process start. A
// calendar trigger without tz uses it.
var hostZone = time.Local

var (
	atPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)
	// tzNamePattern accepts IANA-style names only: letters, digits, _, +, -,
	// separated by single slashes, starting with a letter (so no fixed offset,
	// no absolute or relative path).
	tzNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z][A-Za-z0-9_+-]*)*$`)

	weekdayNames = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	weekdays     = map[string]time.Weekday{
		"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
		"fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
	}
	onMissedPolicies = []string{"run-now", "next-slot"}
)

// buildCalendarTrigger validates a kind = "calendar" trigger and returns it. All
// problems are aggregated so one config check reports them together.
func buildCalendarTrigger(t *triggerTOML, queryType string) (query.Trigger, error) {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf("calendar trigger: "+format, a...)) }

	if queryType != "timer" {
		errs = append(errs, errors.New("calendar trigger requires type = \"timer\""))
	}

	var ct query.CalendarTrigger
	switch m := atPattern.FindStringSubmatch(t.At); {
	case t.At == "":
		add("at is required (24-hour HH:MM, e.g. at = \"02:00\")")
	case m == nil:
		add("at %q is not a 24-hour HH:MM time (two digits each, e.g. \"02:00\")", t.At)
	default:
		fmt.Sscanf(m[1], "%d", &ct.At.Hour)
		fmt.Sscanf(m[2], "%d", &ct.At.Minute)
	}

	if t.Days != nil {
		if len(t.Days) == 0 {
			add("days must not be empty (omit days for every day)")
		}
		seen := map[string]bool{}
		for _, d := range t.Days {
			wd, ok := weekdays[d]
			switch {
			case !ok:
				add("unknown weekday %q (known: %s)", d, strings.Join(weekdayNames, ", "))
			case seen[d]:
				add("days lists %q twice", d)
			default:
				ct.Days = append(ct.Days, wd)
			}
			seen[d] = true
		}
	}

	ct.Loc = hostZone
	if t.TZ != nil {
		loc, ok := loadIANAZone(*t.TZ)
		if !ok {
			add("tz %q is not an IANA time zone name such as \"America/Los_Angeles\" (a fixed UTC offset is not accepted because it cannot follow daylight saving)", *t.TZ)
		}
		ct.Loc = loc
	}

	switch {
	case t.OnMissed == "":
		add("on_missed is required (run-now fires one catch-up tick when the router was not running or the host was asleep at the slot, next-slot waits for the following slot)")
	case t.OnMissed != onMissedPolicies[0] && t.OnMissed != onMissedPolicies[1]:
		add("on_missed %q is not valid (known: %s)", t.OnMissed, strings.Join(onMissedPolicies, ", "))
	default:
		ct.OnMissed = t.OnMissed
	}

	if t.ExpiresIn != nil {
		if t.ExpiresIn.D <= 0 {
			add("expires_in must be greater than 0")
		}
		ct.ExpiresIn = t.ExpiresIn.D
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return ct, nil
}

// loadIANAZone resolves an IANA zone name; ok is false for an empty name,
// "Local", a fixed offset, a path, or a name the zone database does not know.
func loadIANAZone(name string) (*time.Location, bool) {
	if name == "Local" || !tzNamePattern.MatchString(name) {
		return nil, false
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	return loc, true
}

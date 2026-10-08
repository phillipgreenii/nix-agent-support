// Package zone validates IANA zone names and resolves civil dates and times
// in them. Zone data comes from the standard library: the host's zone files
// when it has them, and the embedded copy (time/tzdata) otherwise, so no zone
// database is committed to the repository and none is required on the host.
package zone

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // the embedded zone database, for hosts without usable zone files

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/civil"
)

// ReasonInvalidZone is the Error.Reason of every rejected zone name.
const ReasonInvalidZone = "invalid_zone"

// Error is the rejection of a zone name.
type Error struct {
	Reason  string // always ReasonInvalidZone
	Name    string // the rejected name
	Message string // one plain sentence
}

func (e *Error) Error() string { return e.Message }

func invalid(name, why string) *Error {
	return &Error{Reason: ReasonInvalidZone, Name: name, Message: fmt.Sprintf("time zone %q is not valid: %s", name, why)}
}

// Zone is a named IANA zone. The zero Zone names nothing and MUST NOT be used.
type Zone struct {
	name string
	loc  *time.Location
}

// Load returns the zone called name. A name is valid when time.LoadLocation
// resolves it in its exact letter case, except that the empty string and
// "Local" are rejected: they are escapes to an implicit zone, not zone names.
func Load(name string) (Zone, error) {
	switch name {
	case "":
		return Zone{}, invalid(name, "a zone MUST be named; there is no default zone")
	case "Local":
		return Zone{}, invalid(name, "Local is the host's zone, not a zone name; name the zone explicitly")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return Zone{}, invalid(name, "the name is not a zone path such as America/New_York")
		}
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return Zone{}, invalid(name, "the standard library does not know it; use a region name such as America/New_York")
	}
	if !exactSpelling(name) {
		return Zone{}, invalid(name, "the spelling does not match the zone name's exact letter case")
	}
	return Zone{name: name, loc: loc}, nil
}

// Name returns the name the zone was loaded by.
func (z Zone) Name() string { return z.name }

// Location returns the zone's rules.
func (z Zone) Location() *time.Location { return z.loc }

// Today returns the civil date of the instant now in the zone.
func Today(z Zone, now time.Time) civil.Date {
	l := now.In(z.loc)
	return civil.Date{Year: l.Year(), Month: l.Month(), Day: l.Day()}
}

// hostZoneDirs returns the directories time.LoadLocation reads zone files
// from, in its order.
func hostZoneDirs() []string {
	dirs := make([]string, 0, 5)
	if d := os.Getenv("ZONEINFO"); d != "" {
		dirs = append(dirs, d)
	}
	return append(dirs, "/usr/share/zoneinfo", "/usr/share/lib/zoneinfo", "/usr/lib/locale/TZ", "/etc/zoneinfo")
}

type spelling int

const (
	spellingAbsent spelling = iota // the directory does not hold the name in any case
	spellingWrongCase
	spellingExact
)

// exactSpelling reports whether name is spelled exactly. A case-insensitive
// host file system lets time.LoadLocation resolve odd-cased names, so each
// path segment is confirmed against a directory listing. When no host
// directory holds the name, the embedded copy (or the toolchain's zip) decided
// and both are case-sensitive.
func exactSpelling(name string) bool {
	segs := strings.Split(name, "/")
	wrongCase := false
	for _, dir := range hostZoneDirs() {
		switch spellingIn(dir, segs) {
		case spellingExact:
			return true
		case spellingWrongCase:
			wrongCase = true
		case spellingAbsent:
		}
	}
	return !wrongCase
}

func spellingIn(root string, segs []string) spelling {
	dir := root
	wrongCase := false
	for _, seg := range segs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return spellingAbsent
		}
		match, folded := "", ""
		for _, e := range entries {
			if e.Name() == seg {
				match = seg
				break
			}
			if folded == "" && strings.EqualFold(e.Name(), seg) {
				folded = e.Name()
			}
		}
		if match == "" {
			if folded == "" {
				return spellingAbsent
			}
			wrongCase = true
			match = folded
		}
		dir = filepath.Join(dir, match)
	}
	if wrongCase {
		return spellingWrongCase
	}
	return spellingExact
}

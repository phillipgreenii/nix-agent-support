package zone

import (
	"fmt"
	"strings"
)

// localtimePath is the machine's local-time link.
const localtimePath = "/etc/localtime"

// Host discovers the machine's zone for a client that defaults it. It reads
// the TZ variable (one leading ':' dropped) and, when that is not a valid zone
// name, the target of the local-time link, whose zone name is the path after
// the last "zoneinfo/" segment. It never guesses: when neither names a valid
// zone it fails with a message naming both inputs.
func Host(getenv func(string) string, readlink func(string) (string, error)) (Zone, error) {
	tz := getenv("TZ")
	if name := strings.TrimPrefix(tz, ":"); name != "" {
		if z, err := Load(name); err == nil {
			return z, nil
		}
	}

	target, linkErr := readlink(localtimePath)
	linkDesc := fmt.Sprintf("%s is not readable (%v)", localtimePath, linkErr)
	if linkErr == nil {
		if name, ok := zoneAfterZoneinfo(target); ok {
			if z, err := Load(name); err == nil {
				return z, nil
			}
		}
		linkDesc = fmt.Sprintf("%s points to %q, which names no zone", localtimePath, target)
	}
	return Zone{}, fmt.Errorf("cannot determine the host time zone: TZ=%q is not a zone name and %s; name the zone explicitly", tz, linkDesc)
}

// zoneAfterZoneinfo returns the path after the last "zoneinfo/" path segment.
func zoneAfterZoneinfo(target string) (string, bool) {
	const marker = "zoneinfo/"
	for i := len(target) - len(marker); i >= 0; i-- {
		if target[i:i+len(marker)] == marker && (i == 0 || target[i-1] == '/') {
			name := target[i+len(marker):]
			return name, name != ""
		}
	}
	return "", false
}

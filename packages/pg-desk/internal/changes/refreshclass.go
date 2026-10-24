package changes

import (
	"regexp"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
)

// The refresh error classes: the machine-readable answer to "what kind of
// failure was this", printed by `pg-desk <type> refresh` so that a transient
// forge failure is never mistaken for an entity that is not found. They are a
// subset of the lane run counter's class vocabulary (lanestate.Classes).
const (
	RefreshClassNotFound        = "not_found"
	RefreshClassTransient       = "transient"
	RefreshClassDegraded        = "degraded"
	RefreshClassUnauthenticated = "unauthenticated"
	RefreshClassHydrateError    = "hydrate_error"
)

// wireCodeRe finds the pg-connector wire code in a gather error, which renders
// "pg-connector [args]: exit N: <code>: <message>".
var wireCodeRe = regexp.MustCompile(`exit \d+: ([a-z_]+):`)

// RefreshClass classifies the outcome of one hydration. It returns "" for a
// clean one, RefreshClassDegraded for a degraded read (degraded non-empty with
// no error), and otherwise the class of err. The connector's own wire code
// decides first, because its message text can mention anything: unavailable is
// transient (the forge answered with a failure that a later attempt may
// clear), unauthenticated needs a person, not_found is an entity the source
// says is absent, and every other code is a hydrate_error. Without a wire
// code, a killed or timed-out connector child and a busy store are transient,
// the text "not found"/"not_found" is not_found, and anything else is a
// hydrate_error.
func RefreshClass(err error, degraded string) string {
	if err == nil {
		if degraded != "" {
			return RefreshClassDegraded
		}
		return ""
	}
	msg := err.Error()
	if m := wireCodeRe.FindStringSubmatch(msg); m != nil {
		switch m[1] {
		case "unavailable":
			return RefreshClassTransient
		case "unauthenticated":
			return RefreshClassUnauthenticated
		case "not_found":
			return RefreshClassNotFound
		default:
			return RefreshClassHydrateError
		}
	}
	switch pipeline.ClassifyError(err) {
	case pipeline.ClassKilled, pipeline.ClassDeadline, pipeline.ClassCanceled, pipeline.ClassStoreBusy:
		return RefreshClassTransient
	}
	if strings.Contains(msg, "not found") || strings.Contains(msg, "not_found") {
		return RefreshClassNotFound
	}
	return RefreshClassHydrateError
}

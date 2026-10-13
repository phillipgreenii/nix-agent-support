package metered

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// isRateLimitText reports a primary or secondary rate-limit answer by its
// (lower-cased) text: "rate limit" or "abuse detection". It re-implements the
// unexported github.isRateLimitText of internal/github/review_write.go, which
// this package may not reach and does not change; TestIsRateLimitTextMatchesTheGithubPackage
// fails if the two rules drift apart.
func isRateLimitText(lower string) bool {
	return strings.Contains(lower, "rate limit") || strings.Contains(lower, "abuse detection")
}

// isRefusal reports whether err is, or wraps, a *governor.Refusal: the daemon's
// own decision not to spend, never an answer from the origin.
func isRefusal(err error) bool { return errors.Is(err, governor.ErrRefused) }

// IsRateLimit reports whether err is a rate-limit answer from the origin: a
// *ratefeed.RateLimitError, or an error whose text says "rate limit" or "abuse
// detection". A *governor.Refusal is never one, even though the text of a refusal
// for a paused bucket names the rate limit that caused the pause: a refusal is the
// daemon's own decision and MUST NOT be reclassified or extend that pause.
func IsRateLimit(err error) bool {
	if err == nil || isRefusal(err) {
		return false
	}
	var typed *ratefeed.RateLimitError
	if errors.As(err, &typed) {
		return true
	}
	return isRateLimitText(strings.ToLower(err.Error()))
}

// Classify turns a rate-limit answer into a pause and a typed error. gh calls
// every HTTP 403 an authentication failure, so a rate limit that reaches the
// daemon as gh's error would be read as "run gh auth login"; Classify recognizes
// it by its TEXT ("rate limit" or "abuse detection", never an HTTP 403 by itself)
// and returns a *ratefeed.RateLimitError{Secondary: true, RetryAfter: fallback}
// that does not wrap the authentication sentinel. It also calls gov.PauseAll for
// fallback (the answer's Retry-After is invisible here: every GraphQL and
// porcelain answer lacks it), once per classified error. A fallback of zero or
// less means DefaultFallbackPause.
//
// Everything else comes back unchanged: nil; a *governor.Refusal (checked first:
// its text can carry rate-limit words and it MUST NOT extend a pause); an error
// that is, or wraps, a *ratefeed.RateLimitError already (the Feed paused for it);
// and any error whose text is not a rate-limit answer, an HTTP 403 included.
//
// The text judged is the error's whole message, which for an exec failure begins
// with the gh command line: the rule of the Provider's own isRateLimitText, kept
// so both read the same answers. A call whose own arguments say "rate limit" (a
// search phrase) and that fails for another reason is therefore misread.
func Classify(gov *governor.Governor, fallback time.Duration, err error) error {
	if err == nil || isRefusal(err) {
		return err
	}
	var typed *ratefeed.RateLimitError
	if errors.As(err, &typed) {
		return err
	}
	if !isRateLimitText(strings.ToLower(err.Error())) {
		return err
	}
	if fallback <= 0 {
		fallback = DefaultFallbackPause
	}
	if gov != nil {
		gov.PauseAll(fallback, fmt.Sprintf("rate limit answer without a Retry-After (secondary): background paused %s", fallback))
	}
	return &ratefeed.RateLimitError{Secondary: true, RetryAfter: fallback}
}

// IsNotFound reports whether err says the thing asked for does not exist: its
// text carries "could not resolve to a" (the GraphQL unresolved-node error of an
// id-based read) or "http 404" (a REST 404), the two phrasings verified against
// gh for a genuinely missing PR, comment or run, as opposed to an authentication,
// rate-limit or transport failure.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "could not resolve to a") || strings.Contains(msg, "http 404")
}

// wireError is an error already mapped onto one of scriptout's wire sentinels.
// It reads like scriptout.WrapError's and keeps the error it came from in its
// chain, so a caller can still inspect the refusal or the authentication failure
// behind the code.
type wireError struct {
	sentinel error
	msg      string
	cause    error
}

func (e *wireError) Error() string   { return e.sentinel.Error() + ": " + e.msg }
func (e *wireError) Unwrap() []error { return []error{e.sentinel, e.cause} }

// WireError maps an error of the Provider or of a metered call onto scriptout's
// closed seven-value taxonomy (no code of its own is introduced). It is the one
// mapping every op of the daemon uses, the old backend's classifyGHError extended:
//
//   - a *governor.Refusal is unavailable, with the refusal's reason, which names
//     the cap, the background share, the reserve or the pause;
//   - a rate-limit answer (*ratefeed.RateLimitError, or one recognized by its
//     text) is unavailable, naming the pause or the reset. It is checked BEFORE
//     the authentication sentinel because gh calls every HTTP 403 an
//     authentication failure;
//   - an authentication failure that is not a rate-limit answer is unauthenticated;
//   - a missing PR (IsNotFound) is not_found;
//   - anything else passes through unchanged, for scriptout's own fallback to
//     unavailable.
func WireError(err error) error {
	if err == nil {
		return nil
	}
	var refusal *governor.Refusal
	if errors.As(err, &refusal) {
		return &wireError{sentinel: scriptout.ErrUnavailable, msg: refusal.Reason, cause: err}
	}
	if IsRateLimit(err) {
		return &wireError{sentinel: scriptout.ErrUnavailable, msg: err.Error(), cause: err}
	}
	if errors.Is(err, github.ErrGHAuthInvalid) {
		return &wireError{sentinel: scriptout.ErrUnauthenticated, msg: err.Error(), cause: err}
	}
	if IsNotFound(err) {
		return &wireError{sentinel: scriptout.ErrNotFound, msg: err.Error(), cause: err}
	}
	return err
}

// Package backoff implements the exponential-backoff-with-a-cap retry cadence
// pr-pool uses on both sides of a failure it itself schedules the retry for
// (pg2-0c8yz): the HANDLER RETRY CADENCE (how long the core waits before
// re-offering an event a handler pre-accept declined, INV-FAIL-2) and the
// PULL-SOURCE FAILURE BACKOFF (how long a scheduled query waits before retrying
// after it failed, INV-FAIL-3). Both surfaces share this one SHAPE — short
// initial wait, growing by a fixed factor on each consecutive failure, capped at
// a maximum — because the shape was settled once for both; only the DEFAULT
// VALUES and how many consecutive failures they bound differ per surface. The
// concrete default values are a realization decision
// (`phillipgreenii-nix-agent-support · packages/pr-pool/docs/decisions ·
// DEC-RETRY-1`), not restated in the behavior docs (the floor excludes tuning
// constants).
package backoff

import "time"

// Policy is an exponential-backoff-with-a-cap schedule: Initial is the wait
// after the FIRST consecutive failure, Factor multiplies it on each further
// consecutive failure, and Max caps the growth. A zero Policy is usable
// directly — Duration sanitizes it against Default() — so a caller MAY leave a
// Policy unset and get sane behavior rather than a tight retry loop or
// unbounded growth.
type Policy struct {
	Initial time.Duration
	Factor  float64
	Max     time.Duration
}

// Default returns pr-pool's chosen shape for an unconfigured handler retry
// cadence (DEC-RETRY-1): seconds-to-low-minutes, never hours, appropriate for
// an interactive dev-workflow tool.
func Default() Policy {
	return Policy{Initial: 5 * time.Second, Factor: 2.0, Max: 2 * time.Minute}
}

// sanitized fills any zero/invalid field from Default(), so a partially
// specified Policy (e.g. only Factor overridden) still yields a usable
// schedule rather than an Initial of 0 (a tight retry loop) or a Factor <= 1
// (growth that never climbs to Max).
func (p Policy) sanitized() Policy {
	d := Default()
	if p.Initial <= 0 {
		p.Initial = d.Initial
	}
	if p.Factor <= 1 {
		p.Factor = d.Factor
	}
	if p.Max <= 0 {
		p.Max = d.Max
	}
	return p
}

// Duration returns how long to wait before the given consecutive-failure
// attempt: attempt 1 (the first failure/decline) waits Initial; each further
// consecutive attempt multiplies the previous wait by Factor, capped at Max.
// attempt <= 1 is treated as 1.
func (p Policy) Duration(attempt int) time.Duration {
	p = p.sanitized()
	if attempt < 1 {
		attempt = 1
	}
	d := p.Initial
	for i := 1; i < attempt; i++ {
		if d >= p.Max {
			return p.Max
		}
		d = time.Duration(float64(d) * p.Factor)
	}
	if d > p.Max {
		return p.Max
	}
	return d
}

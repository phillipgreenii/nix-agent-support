package governor

import (
	"errors"
	"fmt"
	"time"
)

// Bucket is one origin allowance, metered separately by the origin. One daemon
// draws all of them on one token.
type Bucket string

const (
	// BucketGraphQL is the point-metered GraphQL allowance (per hour).
	BucketGraphQL Bucket = "graphql"
	// BucketRESTCore is the REST core request allowance (per hour).
	BucketRESTCore Bucket = "rest_core"
	// BucketRESTSearch is the REST search request allowance (per minute).
	BucketRESTSearch Bucket = "rest_search"
)

// Buckets lists every bucket in a stable order.
func Buckets() []Bucket {
	return []Bucket{BucketGraphQL, BucketRESTCore, BucketRESTSearch}
}

// unit names what a bucket counts, for refusal reasons.
func (b Bucket) unit() string {
	if b == BucketGraphQL {
		return "points"
	}
	return "requests"
}

// Class is a priority class, highest first (INV-GH-BUDGET-2). Classes 1 and 2
// are the foreground; 3 to 5 are the background and stop at the background
// share of each cap, leave a pause unspent, and are the only ones a secondary
// limit holds back.
type Class int

const (
	// ClassInteractive (1): a caller is waiting.
	ClassInteractive Class = iota + 1
	// ClassInvalidated (2): write-invalidated, refresh-requested, and the
	// dependent groups a pending change row waits on.
	ClassInvalidated
	// ClassQuery (3): due re-runs of configured queries and overdue members
	// of a configured query.
	ClassQuery
	// ClassKeepAlive (4): overdue entities in the keep-alive set.
	ClassKeepAlive
	// ClassFill (5): entities due soon, used only to fill spare slots of a
	// batch that is already going out.
	ClassFill
)

// Valid reports whether c is one of the five classes.
func (c Class) Valid() bool { return c >= ClassInteractive && c <= ClassFill }

// Background reports whether c is a background class (3 to 5).
func (c Class) Background() bool { return c >= ClassQuery && c <= ClassFill }

// String names the class for logs: "1".."5" with its role.
func (c Class) String() string {
	switch c {
	case ClassInteractive:
		return "1 interactive"
	case ClassInvalidated:
		return "2 invalidated"
	case ClassQuery:
		return "3 query"
	case ClassKeepAlive:
		return "4 keepalive"
	case ClassFill:
		return "5 fill"
	default:
		return fmt.Sprintf("%d invalid", int(c))
	}
}

// Purpose says what an origin call was for. The values are the event log's
// `purpose` vocabulary; the governor does not interpret them (the class does
// the gating), it records spend per purpose.
type Purpose string

const (
	PurposeInteractive Purpose = "interactive"
	PurposeWrite       Purpose = "write"
	PurposeQuery       Purpose = "query"
	PurposeRefresh     Purpose = "refresh"
	PurposeSettle      Purpose = "settle"
	PurposeFill        Purpose = "fill"
	PurposePassthrough Purpose = "passthrough"
)

// Reading is one observation of a bucket by the origin: what is left, and when
// the origin replenishes it.
type Reading struct {
	Bucket Bucket
	// Limit is the bucket's total per window as the origin reports it; zero
	// when unknown. It is informational (State shows it).
	Limit int64
	// Remaining is what the origin says is left. It includes spend by every
	// process on the token, which is how another process draining the token
	// tightens this governor's admission.
	Remaining int64
	// ResetAt is when the origin replenishes the bucket; zero when unknown.
	// A reading is not used past it (the bucket is full again).
	ResetAt time.Time
	// At is when the reading was taken; zero means the governor stamps it
	// with its clock when it is fed in.
	At time.Time
}

// Limits are one bucket's settings.
type Limits struct {
	// Cap is the most this daemon spends from the bucket per Window. It is
	// hard for every class. It MUST be positive.
	Cap int64
	// Reserve is the floor of the bucket's observed remaining: nothing
	// spends below it. For the GraphQL bucket it is rate_reserve_points; for
	// the REST buckets the caps already leave room for other users of the
	// token, so it is normally zero. It MUST NOT be negative.
	Reserve int64
	// Window is how long spend counts against Cap. Zero means the bucket's
	// default: an hour for the GraphQL and REST core buckets, a minute for
	// REST search. The window is rolling, not aligned to the clock hour, so
	// no 60-minute interval ever holds more than Cap, restart or not.
	Window time.Duration
}

// Default windows of the buckets, used when Limits.Window is zero.
const (
	DefaultWindow       = time.Hour
	DefaultSearchWindow = time.Minute
	// DefaultRetention is how long budget rows are kept in the store when
	// Config.Retention is zero.
	DefaultRetention = 24 * time.Hour
)

// Config is the governor's settings.
type Config struct {
	GraphQL    Limits
	RESTCore   Limits
	RESTSearch Limits
	// BackgroundShare is the fraction of each bucket's Cap that the
	// background classes may spend; the rest stays for classes 1 and 2. It
	// MUST be in (0, 1].
	BackgroundShare float64
	// Retention is how long budget rows are kept in the store. Zero means
	// DefaultRetention; it is never shorter than the longest window.
	Retention time.Duration
}

func (c Config) limits(b Bucket) Limits {
	var l Limits
	switch b {
	case BucketGraphQL:
		l = c.GraphQL
	case BucketRESTCore:
		l = c.RESTCore
	case BucketRESTSearch:
		l = c.RESTSearch
	}
	if l.Window <= 0 {
		l.Window = DefaultWindow
		if b == BucketRESTSearch {
			l.Window = DefaultSearchWindow
		}
	}
	return l
}

// Validate reports the first thing wrong with c.
func (c Config) Validate() error {
	for _, b := range Buckets() {
		l := c.limits(b)
		if l.Cap <= 0 {
			return fmt.Errorf("governor: %s cap must be positive, got %d", b, l.Cap)
		}
		if l.Reserve < 0 {
			return fmt.Errorf("governor: %s reserve must not be negative, got %d", b, l.Reserve)
		}
	}
	if c.BackgroundShare <= 0 || c.BackgroundShare > 1 {
		return fmt.Errorf("governor: background share must be in (0, 1], got %v", c.BackgroundShare)
	}
	return nil
}

// Request is one prospective spend.
type Request struct {
	Bucket Bucket
	Class  Class
	// Cost is the estimated spend in the bucket's unit (points, or requests);
	// anything below 1 counts as 1.
	Cost int64
}

// Spend is one origin call that happened, to record.
type Spend struct {
	Bucket Bucket
	Class  Class
	// Purpose, Group and BatchSize describe the call for the observers; Group
	// is the field group ("" when the call is not tied to one) and BatchSize
	// the number of entities it carried.
	Purpose   Purpose
	Group     string
	BatchSize int
	// Cost is what the call spent: the response's rateLimit cost for GraphQL,
	// one per REST request.
	Cost int64
	// Reading is the rate-limit reading the response carried, nil when it
	// carried none.
	Reading *Reading
	// Outcome is a short word for the observers ("ok", "error",
	// "rate_limited"); the governor records it and does not act on it.
	Outcome string
	// At is when the call finished; zero means the governor's clock.
	At time.Time
}

// SpendEvent is what the spend observer receives for every recorded call.
type SpendEvent struct {
	At        time.Time
	Bucket    Bucket
	Class     Class
	Purpose   Purpose
	Group     string
	BatchSize int
	Cost      int64
	// Remaining is the best-known remaining of the bucket after the call; -1
	// when no reading is in force.
	Remaining int64
	Outcome   string
}

// BucketState is one bucket's snapshot.
type BucketState struct {
	Bucket Bucket
	Cap    int64
	// BackgroundCap is the most the background classes may spend: the cap
	// times the background share.
	BackgroundCap int64
	Reserve       int64
	Window        time.Duration
	// Spent and BackgroundSpent are the spend inside the window; InFlight is
	// what admitted but unsettled requests have reserved.
	Spent           int64
	BackgroundSpent int64
	InFlight        int64
	// SpentByPurpose is the spend inside the window per purpose.
	SpentByPurpose map[Purpose]int64
	// Remaining and ResetAt are the origin's reading in force (Remaining is
	// -1 and ResetAt zero when none is); Limit is the reported total.
	Remaining int64
	Limit     int64
	ResetAt   time.Time
	// Paused, PauseReason and PausedUntil describe a hold on the background
	// classes.
	Paused      bool
	PauseReason string
	PausedUntil time.Time
}

// State is a snapshot of the governor.
type State struct {
	At      time.Time
	Buckets []BucketState
}

// Bucket returns the snapshot of b, and false when there is none.
func (s State) Bucket(b Bucket) (BucketState, bool) {
	for _, bs := range s.Buckets {
		if bs.Bucket == b {
			return bs, true
		}
	}
	return BucketState{}, false
}

// RefusalKind says which rule stopped a request.
type RefusalKind int

const (
	// RefusedCap: the bucket's cap is spent (every class).
	RefusedCap RefusalKind = iota + 1
	// RefusedReserve: the spend would leave the origin's remaining below the
	// reserve (every class).
	RefusedReserve
	// RefusedBackgroundShare: a background class reached the background share
	// of the cap.
	RefusedBackgroundShare
	// RefusedPaused: a secondary limit holds the background classes.
	RefusedPaused
)

func (k RefusalKind) String() string {
	switch k {
	case RefusedCap:
		return "cap"
	case RefusedReserve:
		return "reserve"
	case RefusedBackgroundShare:
		return "background_share"
	case RefusedPaused:
		return "paused"
	default:
		return "unknown"
	}
}

// ErrRefused matches every *Refusal under errors.Is.
var ErrRefused = errors.New("governor: spend refused")

// Refusal is the typed error Admit returns when the governor declines a
// prospective spend. Reason is a sentence a caller can show: it names the cap,
// the background share, the reserve or the pause, as INV-GH-BUDGET-3 and
// INV-GH-BUDGET-5 require ("interactive reads are served stale or answered
// unavailable naming the cap", "... naming the reserve").
type Refusal struct {
	Kind   RefusalKind
	Bucket Bucket
	Class  Class
	Cost   int64
	// Limit is the number that stopped the request: the cap, the background
	// cap or the reserve (zero for a pause).
	Limit  int64
	Reason string
	// RetryAt is the earliest time the same request may be admitted: when
	// enough spend ages out of the window, the reading's reset, or the end of
	// the pause. It is zero when it cannot be known (a request larger than
	// the cap never fits).
	RetryAt time.Time
}

// Error returns the reason.
func (r *Refusal) Error() string { return r.Reason }

// Is makes errors.Is(err, ErrRefused) true.
func (r *Refusal) Is(target error) bool { return target == ErrRefused }

package shimargs

import (
	"math"
	"strings"
	"time"
)

// UsageLine is the usage text the real caffeinate prints (newline included).
const UsageLine = "usage: caffeinate [-disu] [-t timeout] [-w Process ID] [command arguments...]\n"

// Flags is the set of assertion flags (-d -i -m -s).
type Flags uint8

// Assertion flags, one per caffeinate option letter.
const (
	FlagD Flags = 1 << iota // -d prevent display sleep
	FlagI                   // -i prevent idle sleep
	FlagM                   // -m prevent disk idle sleep
	FlagS                   // -s prevent system sleep (AC power)
)

// Has reports whether every flag in f is set.
func (s Flags) Has(f Flags) bool { return s&f == f }

// String renders the set as option letters in d, i, m, s order.
func (s Flags) String() string {
	var b strings.Builder
	for _, p := range []struct {
		f Flags
		c byte
	}{{FlagD, 'd'}, {FlagI, 'i'}, {FlagM, 'm'}, {FlagS, 's'}} {
		if s.Has(p.f) {
			b.WriteByte(p.c)
		}
	}
	return b.String()
}

// TimeoutKind says whether a -t bound applies.
type TimeoutKind uint8

// Timeout kinds.
const (
	TimeoutNone       TimeoutKind = iota // no -t given
	TimeoutIndefinite                    // -t 0
	TimeoutFinite                        // -t N with N > 0
)

// Timeout is the parsed -t value.
type Timeout struct {
	Kind TimeoutKind
	// D is the positive wall-clock duration when Kind is TimeoutFinite.
	D time.Duration
}

// WatchKind says whether a -w bound applies.
type WatchKind uint8

// Watch kinds.
const (
	WatchNone       WatchKind = iota // no -w given
	WatchIndefinite                  // -w 0
	WatchPID                         // -w PID with PID > 0 that exists
)

// Watch is the parsed -w value.
type Watch struct {
	Kind WatchKind
	// PID is the watched process when Kind is WatchPID.
	PID int
}

// Request is a lease request: the command line asked for a keep-awake hold.
type Request struct {
	// Flags is the assertion flag set; FlagI alone when none was given.
	Flags Flags
	// Timeout and Watch are the bounds of the pure form. In the utility form
	// the shim ignores both (the lease is bound to the child), so Parse leaves
	// them at their zero values there.
	Timeout Timeout
	Watch   Watch
	// Utility is the command and its arguments; empty for the pure form.
	Utility []string
}

// OutcomeKind classifies a command line that does not request a lease.
type OutcomeKind uint8

// Outcome kinds.
const (
	OutcomeUsage         OutcomeKind = iota + 1 // bad or missing option value: usage line, exit 1
	OutcomeIllegalOption                        // unknown option letter: message plus usage line, exit 1
	OutcomeMissingArg                           // -t or -w without a value: message plus usage line, exit 1
	OutcomeRejectedU                            // -u is not supported by the shim: exit 1
	OutcomeNoLeaseTime                          // -t negative or overflowing: exit 0 at once
	OutcomeNoLeasePID                           // -w -1 or a nonexistent pid: exit 0 at once
)

// Outcome is the exact result of a command line that yields no lease.
type Outcome struct {
	Kind OutcomeKind
	// Stderr is the exact text to print (empty for the no-lease kinds).
	Stderr string
	// ExitCode is the process exit status.
	ExitCode int
}

// Message for a rejected -u.
const rejectedUMessage = "caffeinate: -u is not supported by pg-caffeinate-shim\n"

// maxTimeoutSeconds is the largest -t the shim can hold as a time.Duration.
// The real tool wraps its own arithmetic chaotically for huge values
// (observed 2026-10-14: 1e15 holds, 1e17 and 1e18 return at once, 4e18 holds,
// 9e18 returns at once), so no exact boundary is mirrored: anything the shim
// cannot represent is treated as overflow, exit 0.
const maxTimeoutSeconds = math.MaxInt64 / int64(time.Second)

// Parse parses args (without argv[0]) with getopt semantics: combined flags
// (-dims), attached values (-t300, -it300), "--" ending options, and option
// parsing stopping at the first non-option. exists reports whether a pid is
// in the process table; a nil exists treats every pid as present.
//
// Exactly one of the returns is meaningful: a non-nil Outcome means the
// command line yields no lease and the Request is zero.
func Parse(args []string, exists func(pid int) bool) (Request, *Outcome) {
	var (
		flags   Flags
		sawU    bool
		timeout *int64
		watch   *int64
	)
	i := 0
loop:
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if len(a) < 2 || a[0] != '-' {
			break // first non-option (a lone "-" is a non-option)
		}
		for j := 1; j < len(a); j++ {
			switch c := a[j]; c {
			case 'd':
				flags |= FlagD
			case 'i':
				flags |= FlagI
			case 'm':
				flags |= FlagM
			case 's':
				flags |= FlagS
			case 'u':
				sawU = true
			case 't', 'w':
				var val string
				switch {
				case j+1 < len(a):
					val = a[j+1:]
				case i+1 < len(args):
					i++
					val = args[i]
				default:
					return Request{}, &Outcome{
						Kind: OutcomeMissingArg, ExitCode: 1,
						Stderr: "caffeinate: option requires an argument -- " + string(c) + "\n" + UsageLine,
					}
				}
				n, ok := strtol(val)
				if !ok {
					return Request{}, &Outcome{Kind: OutcomeUsage, Stderr: UsageLine, ExitCode: 1}
				}
				if c == 't' {
					timeout = &n
				} else {
					watch = &n
				}
				i++
				continue loop
			default:
				return Request{}, &Outcome{
					Kind: OutcomeIllegalOption, ExitCode: 1,
					Stderr: "caffeinate: illegal option -- " + string([]byte{c}) + "\n" + UsageLine,
				}
			}
		}
		i++
	}
	utility := args[i:]

	if sawU {
		return Request{}, &Outcome{Kind: OutcomeRejectedU, Stderr: rejectedUMessage, ExitCode: 1}
	}
	if flags == 0 {
		flags = FlagI
	}
	req := Request{Flags: flags}
	if len(utility) > 0 {
		req.Utility = append([]string(nil), utility...)
		return req, nil
	}

	if timeout != nil {
		switch n := *timeout; {
		case n < 0, n > maxTimeoutSeconds:
			return Request{}, &Outcome{Kind: OutcomeNoLeaseTime}
		case n == 0:
			req.Timeout = Timeout{Kind: TimeoutIndefinite}
		default:
			req.Timeout = Timeout{Kind: TimeoutFinite, D: time.Duration(n) * time.Second}
		}
	}
	if watch != nil {
		// The real tool stores the value in a 32-bit pid_t (observed
		// 2026-10-14: -w 4294967297 watches pid 1, -w 2147483648 is negative).
		switch pid := int32(*watch); {
		case pid < 0:
			return Request{}, &Outcome{Kind: OutcomeNoLeasePID}
		case pid == 0:
			req.Watch = Watch{Kind: WatchIndefinite}
		default:
			if exists != nil && !exists(int(pid)) {
				return Request{}, &Outcome{Kind: OutcomeNoLeasePID}
			}
			req.Watch = Watch{Kind: WatchPID, PID: int(pid)}
		}
	}
	return req, nil
}

// strtol mirrors C strtol with base 0: optional leading whitespace, an
// optional sign, then 0x/0X hex, a leading 0 octal, or decimal, stopping at
// the first byte that does not fit (trailing text is ignored). ok is false
// when no conversion happened. Out-of-range values clamp to the int64 limits,
// as strtol does.
func strtol(s string) (n int64, ok bool) {
	i := 0
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	base := uint64(10)
	switch {
	case i+2 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') && digitVal(s[i+2]) < 16:
		base = 16
		i += 2
	case i < len(s) && s[i] == '0':
		base = 8
	}
	start := i
	var mag uint64
	over := false
	for ; i < len(s); i++ {
		d := digitVal(s[i])
		if d >= base {
			break
		}
		if mag > (math.MaxUint64-d)/base {
			over = true
			continue
		}
		mag = mag*base + d
	}
	if i == start {
		return 0, false
	}
	switch {
	case neg && (over || mag > 1<<63):
		return math.MinInt64, true
	case neg:
		return -int64(mag), true // mag == 1<<63 wraps to MinInt64, as intended
	case over || mag > math.MaxInt64:
		return math.MaxInt64, true
	}
	return int64(mag), true
}

func isSpace(c byte) bool {
	return c == ' ' || (c >= '\t' && c <= '\r')
}

// digitVal returns the digit's value, or 255 for a non-digit.
func digitVal(c byte) uint64 {
	switch {
	case c >= '0' && c <= '9':
		return uint64(c - '0')
	case c >= 'a' && c <= 'f':
		return uint64(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return uint64(c-'A') + 10
	}
	return 255
}

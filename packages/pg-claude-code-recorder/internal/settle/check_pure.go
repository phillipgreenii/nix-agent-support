package settle

import (
	"fmt"
	"time"
)

// Pure predicates of the stage machine (evaluate) and of the settle-time checks
// (checkSnapshot, checkAppend). The shell keeps every read and commit and their
// order; these functions only answer the yes/no or which-reason questions.

// quietAgeReached reports whether a Conversation has been quiet for at least
// the settle.after wait.
func quietAgeReached(now, newest time.Time, after time.Duration) bool {
	return now.Sub(newest) >= after
}

// heldHasNewWrites reports whether a file was written after the hold began
// (sinceNS is the hold time in Unix nanoseconds).
func heldHasNewWrites(newest time.Time, sinceNS int64) bool {
	return newest.After(time.Unix(0, sinceNS))
}

// heldRecheckNotDue reports whether a held Conversation is skipped because its
// last check is more recent than held.recheck. A forced pass, or one with no
// recorded check, is never skipped.
func heldRecheckNotDue(force bool, checkedAt, now time.Time, recheck time.Duration) bool {
	return !force && !checkedAt.IsZero() && now.Sub(checkedAt) < recheck
}

// snapshotFailure compares a snapshot file's original with the stored version
// and returns the failure reason and detail ("" when they agree). Size is
// compared before the hash.
func snapshotFailure(size int64, hash uint64, storedSize int64, storedHash uint64) (reason, detail string) {
	switch {
	case size != storedSize:
		return ReasonCopyOffset, fmt.Sprintf("stored %d bytes, original %d", storedSize, size)
	case hash != storedHash:
		return ReasonCopyTail, "stored version differs from the original"
	}
	return "", ""
}

// appendOriginalFailure compares an append file's original size with the copy
// offset and, when a tail hash is kept, the tail. tailMatches is a function
// because it reads the original file and is only needed when the sizes agree
// and a tail exists.
func appendOriginalFailure(origSize, copyOffset int64, tailLen int, tailMatches func() bool) (reason, detail string) {
	switch {
	case origSize != copyOffset:
		return ReasonCopyOffset, fmt.Sprintf("copy offset %d, original %d bytes", copyOffset, origSize)
	case tailLen > 0:
		if !tailMatches() {
			return ReasonCopyTail, "tail hash differs from the original"
		}
	}
	return "", ""
}

// tornFactStale reports whether a recorded torn-tail fact no longer describes
// the copy: it starts away from the last newline, or no longer ends at the copy
// offset (later growth invalidates it).
func tornFactStale(factOff, factLen, lastNewline, copyOffset int64) bool {
	return factOff != lastNewline || factOff+factLen != copyOffset
}

// tornVerdict is the outcome of looking at the unparsed tail of a copy.
type tornVerdict struct {
	pending  bool // an unparsed tail exists: the pending event is emitted
	tooLong  bool // the tail is at least MaxLineBytes: a failure
	tryParse bool // the Conversation is quiet and the tail is short: parse it
}

// tornTail judges the bytes after the last newline (tornLen) of a copy that is
// parsed up to parseOffset. A tail is pending when it has bytes and the parser
// is behind the copy. A tail of maxLine bytes or more is too long to wait for;
// a shorter one is parsed once the Conversation is quiet.
func tornTail(tornLen, parseOffset, copyOffset int64, maxLine int, quiet bool) tornVerdict {
	if tornLen <= 0 || parseOffset >= copyOffset {
		return tornVerdict{}
	}
	v := tornVerdict{pending: true}
	switch {
	case tornLen >= int64(maxLine):
		v.tooLong = true
	case quiet:
		v.tryParse = true
	}
	return v
}

// parseBehindFailure reports whether the parse offset is neither at the last
// newline nor at the copy offset, which the check only asks of a tail that is
// short enough to be waited for, or one the parser has already reached.
func parseBehindFailure(parseOffset, copyOffset, lastNewline, tornLen int64, maxLine int) bool {
	if tornLen < int64(maxLine) || parseOffset >= copyOffset {
		return parseOffset != lastNewline && parseOffset != copyOffset
	}
	return false
}

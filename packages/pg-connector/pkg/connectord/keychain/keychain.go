// Package keychain is the shared Port for reading one generic password from
// the macOS login keychain. Keychain is the seam, Security is the production
// Adapter that runs the system security tool, and Fake is the scripted
// Adapter for tests.
//
// Errors never carry the tool's stdout or stderr: a failed read is reported as
// an *Error holding a Kind and the exit code, with fixed text only, so a token
// printed by a misbehaving executable cannot reach a log line.
package keychain

import (
	"context"
	"errors"
	"fmt"
)

// Keychain reads one generic password.
type Keychain interface {
	Password(ctx context.Context, service, account string) (string, error)
}

// Kind classifies a failed keychain read.
type Kind string

const (
	// KindLocked means the keychain is locked or user interaction is not
	// allowed (for example a locked keychain at boot).
	KindLocked Kind = "locked"
	// KindItemMissing means no item matches the service and account.
	KindItemMissing Kind = "item_missing"
	// KindTimeout means the read did not finish within the time limit.
	KindTimeout Kind = "timeout"
	// KindOther is every failure not mapped above.
	KindOther Kind = "other"
)

// Exit codes of security(1) find-generic-password. The tool exits with the
// OSStatus of the failure truncated to its low byte, and security(1) does not
// tabulate them. errSecItemNotFound (-25300) gives 44, errSecInteractionNotAllowed
// (-25308) gives 36 and errSecAuthFailed (-25293) gives 51.
const (
	exitInteractionNotAllowed = 36
	exitItemNotFound          = 44
	exitAuthFailed            = 51
)

// KindForExitCode maps a security exit code to a Kind; unmapped is KindOther.
func KindForExitCode(code int) Kind {
	switch code {
	case exitItemNotFound:
		return KindItemMissing
	case exitInteractionNotAllowed, exitAuthFailed:
		return KindLocked
	default:
		return KindOther
	}
}

// Error is a failed keychain read. Its text is fixed apart from the kind and
// the exit code.
type Error struct {
	Kind     Kind
	ExitCode int // -1 when the tool did not exit with a code (timeout, start failure)
}

func (e *Error) Error() string {
	return fmt.Sprintf("keychain read failed: kind=%s exit_code=%d", e.Kind, e.ExitCode)
}

// KindOf returns the Kind of err when it wraps an *Error, else KindOther.
func KindOf(err error) Kind {
	var ke *Error
	if errors.As(err, &ke) {
		return ke.Kind
	}
	return KindOther
}

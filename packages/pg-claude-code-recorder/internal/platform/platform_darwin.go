//go:build darwin && cgo

// Package platform holds the platform-specific code. This darwin-only cgo file
// proves the cgo toolchain and the Apple SDK are reachable from mkGoApp; the
// FSEvents watcher and the darwin system calls land here in later packets.
package platform

/*
#cgo LDFLAGS: -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>

static int ccrec_cf_ready(void) {
	return CFAbsoluteTimeGetCurrent() > 0 ? 1 : 0;
}
*/
import "C"

// CgoReady reports whether the CoreFoundation call linked and ran.
func CgoReady() bool { return C.ccrec_cf_ready() == 1 }

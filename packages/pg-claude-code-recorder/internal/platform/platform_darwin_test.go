//go:build darwin && cgo

package platform

import "testing"

func TestCgoReadyOnDarwin(t *testing.T) {
	if !CgoReady() {
		t.Fatal("the darwin cgo file did not link CoreFoundation")
	}
}

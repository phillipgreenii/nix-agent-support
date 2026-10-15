//go:build !darwin

package lease

import "errors"

// errUnsupported is returned by the process helpers off darwin: pg-keep-awake
// is a macOS tool and only builds here so the portable code can be tested.
var errUnsupported = errors.New("process inspection is only implemented on darwin")

// ProcStateOf is unsupported off darwin.
func ProcStateOf(pid int) (ProcState, error) { return ProcState{}, errUnsupported }

// ListPIDs is unsupported off darwin.
func ListPIDs() ([]int, error) { return nil, errUnsupported }

// ProcessInfo is unsupported off darwin.
func ProcessInfo(pid int) (ProcInfo, error) { return ProcInfo{}, errUnsupported }

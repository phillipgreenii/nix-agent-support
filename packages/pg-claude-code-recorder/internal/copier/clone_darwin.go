//go:build darwin

package copier

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	volCapIntClone       = 0x00010000 // VOL_CAP_INT_CLONE
	volCapInterfaces     = 1          // VOL_CAPABILITIES_INTERFACES
	attrCmnextPrivateSz  = 0x00000008 // ATTR_CMNEXT_PRIVATESIZE (in forkattr with FSOPT_ATTR_CMN_EXTENDED)
	fsoptAttrCmnExtended = 0x00000020
)

// SystemClone is the production ClonePolicy: clonefile with CLONE_NOFOLLOW (the
// default follows symbolic links), the VOL_CAP_INT_CLONE probe and
// ATTR_CMNEXT_PRIVATESIZE.
type SystemClone struct{}

func getattrlist(path string, al *unix.Attrlist, buf []byte, opts uintptr) error {
	p, err := unix.BytePtrFromString(path)
	if err != nil {
		return err
	}
	//nolint:staticcheck // x/sys/unix has no getattrlist wrapper; the raw syscall is the only pure-Go route
	_, _, e := unix.Syscall6(unix.SYS_GETATTRLIST, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(al)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), opts, 0)
	if e != 0 {
		return e
	}
	return nil
}

// Probe checks the same st_dev and the volume's clone capability.
func (SystemClone) Probe(stateDir, claudeDir string) (bool, string) {
	if !sameDevice(stateDir, claudeDir) {
		return false, "different volume"
	}
	al := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Volattr: unix.ATTR_VOL_INFO | unix.ATTR_VOL_CAPABILITIES}
	buf := make([]byte, 4+32)
	if err := getattrlist(stateDir, &al, buf, 0); err != nil {
		return false, "capability probe failed: " + err.Error()
	}
	// u32 length, then vol_capabilities_attr_t: capabilities[4] then valid[4].
	caps := binary.LittleEndian.Uint32(buf[4+4*volCapInterfaces:])
	valid := binary.LittleEndian.Uint32(buf[4+16+4*volCapInterfaces:])
	if valid&volCapIntClone == 0 || caps&volCapIntClone == 0 {
		return false, "volume reports no clone capability"
	}
	return true, "same volume, clone capable"
}

// Clone clones src to dst.
func (SystemClone) Clone(src, dst string) error {
	return unix.Clonefileat(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.CLONE_NOFOLLOW)
}

// PrivateSize returns ATTR_CMNEXT_PRIVATESIZE for path (du counts a clone at
// full size; this is the space only the clone holds).
func (SystemClone) PrivateSize(path string) (int64, error) {
	al := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Forkattr: attrCmnextPrivateSz}
	buf := make([]byte, 4+8)
	if err := getattrlist(path, &al, buf, fsoptAttrCmnExtended); err != nil {
		return 0, fmt.Errorf("getattrlist private size: %w", err)
	}
	return int64(binary.LittleEndian.Uint64(buf[4:])), nil
}

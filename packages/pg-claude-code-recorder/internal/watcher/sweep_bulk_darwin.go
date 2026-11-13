//go:build darwin && cgo

package watcher

/*
#include <fcntl.h>
#include <stdlib.h>
#include <string.h>
#include <sys/attr.h>
#include <unistd.h>

static int ccrec_bulk_open(const char *path) {
	return open(path, O_RDONLY | O_DIRECTORY | O_CLOEXEC | O_NOFOLLOW);
}

// FSOPT_PACK_INVAL_ATTRS packs every requested attribute for every entry, so
// the record layout is fixed (parsed by parseBulk).
static int ccrec_bulk_read(int fd, void *buf, size_t n) {
	struct attrlist al;
	memset(&al, 0, sizeof(al));
	al.bitmapcount = ATTR_BIT_MAP_COUNT;
	al.commonattr = ATTR_CMN_RETURNED_ATTRS | ATTR_CMN_NAME | ATTR_CMN_DEVID |
		ATTR_CMN_OBJTYPE | ATTR_CMN_CRTIME | ATTR_CMN_MODTIME | ATTR_CMN_FILEID;
	al.fileattr = ATTR_FILE_DATALENGTH;
	return getattrlistbulk(fd, &al, buf, n, FSOPT_NOFOLLOW | FSOPT_PACK_INVAL_ATTRS);
}
*/
import "C"

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"
	"unsafe"
)

const (
	vtReg = 1 // VREG
	vtDir = 2 // VDIR

	bulkBufSize = 256 * 1024
	// record: u32 length, attribute_set_t (5 x u32), then in attribute-bit order
	// NAME (attrreference_t: i32 offset, u32 length), DEVID i32, OBJTYPE u32,
	// CRTIME timespec, MODTIME timespec, FILEID u64, DATALENGTH i64.
	bulkNameOff = 4 + 20
	bulkMinLen  = bulkNameOff + 8 + 4 + 4 + 16 + 16 + 8 + 8
)

type bulkEntry struct {
	name string
	kind uint32
	info FileInfo
}

// platformSweep lists every regular file under root with getattrlistbulk, which
// returns a whole directory's names and attributes in one call.
func platformSweep(root string, descend func(dir string) bool) ([]FileInfo, error) {
	var out []FileInfo
	buf := make([]byte, bulkBufSize) // one buffer per walk, reused for every directory
	if err := bulkWalk(root, descend, buf, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func bulkWalk(dir string, descend func(dir string) bool, buf []byte, out *[]FileInfo) error {
	noteDirRead(dir)
	entries, err := bulkReadDir(dir, buf)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return fmt.Errorf("bulk read %s: %w", dir, err)
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.name)
		switch e.kind {
		case vtReg:
			e.info.Path = path
			*out = append(*out, e.info)
		case vtDir:
			if descend != nil && !descend(path) {
				continue
			}
			if err := bulkWalk(path, descend, buf, out); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// bulkReadDir fills buf (owned by the caller, never retained) once per
// getattrlistbulk call.
func bulkReadDir(dir string, buf []byte) ([]bulkEntry, error) {
	cdir := C.CString(dir)
	defer C.free(unsafe.Pointer(cdir))
	fd, err := C.ccrec_bulk_open(cdir)
	if fd < 0 {
		return nil, err
	}
	defer C.close(fd)
	var all []bulkEntry
	for {
		n, err := C.ccrec_bulk_read(fd, unsafe.Pointer(&buf[0]), C.size_t(len(buf)))
		if n < 0 {
			return nil, err
		}
		if n == 0 {
			return all, nil
		}
		var perr error
		all, perr = parseBulk(all, buf, int(n))
		if perr != nil {
			return nil, perr
		}
	}
}

// parseBulk appends count records decoded from buf to out. Names are copied,
// so nothing retains buf.
func parseBulk(out []bulkEntry, buf []byte, count int) ([]bulkEntry, error) {
	off := 0
	for i := 0; i < count; i++ {
		if off+4 > len(buf) {
			return nil, errors.New("bulk: truncated record header")
		}
		length := int(binary.LittleEndian.Uint32(buf[off:]))
		if length < bulkMinLen || off+length > len(buf) {
			return nil, fmt.Errorf("bulk: bad record length %d", length)
		}
		rec := buf[off : off+length]
		off += length

		p := bulkNameOff
		nameOff := int(int32(binary.LittleEndian.Uint32(rec[p:])))
		nameLen := int(binary.LittleEndian.Uint32(rec[p+4:]))
		start := p + nameOff
		if nameLen < 1 || start < 0 || start+nameLen > len(rec) {
			return nil, errors.New("bulk: bad name reference")
		}
		name := string(rec[start : start+nameLen-1]) // drop the NUL
		p += 8
		dev := uint64(binary.LittleEndian.Uint32(rec[p:]))
		p += 4
		kind := binary.LittleEndian.Uint32(rec[p:])
		p += 4
		birth := timespecAt(rec[p:])
		p += 16
		mod := timespecAt(rec[p:])
		p += 16
		ino := binary.LittleEndian.Uint64(rec[p:])
		p += 8
		size := int64(binary.LittleEndian.Uint64(rec[p:]))
		out = append(out, bulkEntry{name: name, kind: kind, info: FileInfo{
			Dev: dev, Ino: ino, Birth: birth, Size: size, ModTime: mod,
		}})
	}
	return out, nil
}

func timespecAt(b []byte) time.Time {
	sec := int64(binary.LittleEndian.Uint64(b))
	nsec := int64(binary.LittleEndian.Uint64(b[8:]))
	return time.Unix(sec, nsec)
}

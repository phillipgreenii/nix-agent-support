//go:build darwin && cgo

package watcher

/*
#cgo LDFLAGS: -framework CoreServices -framework CoreFoundation
#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>
#include <stdint.h>
#include <stdlib.h>
#include <sys/stat.h>

extern void ccrecFSEventsCallback(uintptr_t h, size_t n, char **paths, uint32_t *flags, uint64_t *ids);

static void ccrec_cb(ConstFSEventStreamRef s, void *info, size_t n, void *paths,
		const FSEventStreamEventFlags *flags, const FSEventStreamEventId *ids) {
	ccrecFSEventsCallback((uintptr_t)info, n, (char **)paths, (uint32_t *)flags, (uint64_t *)ids);
}

typedef struct {
	FSEventStreamRef stream;
	dispatch_queue_t queue;
} ccrec_stream;

// One stream over every root. Without kFSEventStreamCreateFlagUseCFTypes the
// callback receives plain C strings.
static ccrec_stream *ccrec_start(uintptr_t handle, const char **roots, int nroots,
		uint64_t since, double latency) {
	CFMutableArrayRef arr = CFArrayCreateMutable(NULL, nroots, &kCFTypeArrayCallBacks);
	for (int i = 0; i < nroots; i++) {
		CFStringRef s = CFStringCreateWithCString(NULL, roots[i], kCFStringEncodingUTF8);
		CFArrayAppendValue(arr, s);
		CFRelease(s);
	}
	FSEventStreamContext ctx = {0, (void *)handle, NULL, NULL, NULL};
	FSEventStreamRef st = FSEventStreamCreate(NULL, ccrec_cb, &ctx, arr, since, latency,
		kFSEventStreamCreateFlagFileEvents | kFSEventStreamCreateFlagNoDefer);
	CFRelease(arr);
	if (!st) return NULL;
	ccrec_stream *s = malloc(sizeof(ccrec_stream));
	s->stream = st;
	s->queue = dispatch_queue_create("ccrec.fsevents", DISPATCH_QUEUE_SERIAL);
	FSEventStreamSetDispatchQueue(st, s->queue);
	if (!FSEventStreamStart(st)) {
		FSEventStreamInvalidate(st);
		FSEventStreamRelease(st);
		dispatch_release(s->queue);
		free(s);
		return NULL;
	}
	return s;
}

static void ccrec_stop(ccrec_stream *s) {
	FSEventStreamStop(s->stream);
	FSEventStreamInvalidate(s->stream);
	FSEventStreamRelease(s->stream);
	dispatch_release(s->queue);
	free(s);
}

static int ccrec_volume_uuid(const char *path, char *out, size_t n) {
	struct stat sb;
	if (stat(path, &sb) != 0) return -1;
	CFUUIDRef u = FSEventsCopyUUIDForDevice(sb.st_dev);
	if (!u) return -1;
	CFStringRef s = CFUUIDCreateString(NULL, u);
	int ok = CFStringGetCString(s, out, n, kCFStringEncodingUTF8);
	CFRelease(s);
	CFRelease(u);
	return ok ? 0 : -1;
}
*/
import "C"

import (
	"errors"
	"sync"
	"unsafe"
)

const (
	flagMustScanSubDirs = 0x01
	flagUserDropped     = 0x02
	flagKernelDropped   = 0x04
	flagIDsWrapped      = 0x08
	flagRootChanged     = 0x20
	// flagsNeedSweep are the flags that mean FSEvents lost track of what
	// changed, so only a sweep can recover.
	flagsNeedSweep = flagMustScanSubDirs | flagUserDropped | flagKernelDropped | flagIDsWrapped | flagRootChanged

	sinceNow = ^uint64(0) // kFSEventStreamEventIdSinceNow
)

type fsBackend struct {
	handle uintptr
	stream *C.ccrec_stream
	onHint func(Hint)
}

var (
	fsMu       sync.Mutex
	fsNext     uintptr
	fsRegistry = map[uintptr]*fsBackend{}
)

func newBackend() backend { return &fsBackend{} }

func (b *fsBackend) CanReplay() bool { return true }

func (b *fsBackend) VolumeUUID(path string) string {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var buf [64]C.char
	if C.ccrec_volume_uuid(cpath, &buf[0], C.size_t(len(buf))) != 0 {
		return ""
	}
	return C.GoString(&buf[0])
}

func (b *fsBackend) Start(roots []string, since uint64, replay bool, onHint func(Hint)) error {
	if !replay {
		since = sinceNow
	}
	b.onHint = onHint
	fsMu.Lock()
	fsNext++
	b.handle = fsNext
	fsRegistry[b.handle] = b
	fsMu.Unlock()

	croots := make([]*C.char, len(roots))
	for i, r := range roots {
		croots[i] = C.CString(r)
	}
	defer func() {
		for _, c := range croots {
			C.free(unsafe.Pointer(c))
		}
	}()
	b.stream = C.ccrec_start(C.uintptr_t(b.handle), (**C.char)(unsafe.Pointer(&croots[0])),
		C.int(len(roots)), C.uint64_t(since), C.double(0.2))
	if b.stream == nil {
		fsMu.Lock()
		delete(fsRegistry, b.handle)
		fsMu.Unlock()
		return errors.New("watcher: FSEventStreamCreate/Start failed")
	}
	return nil
}

func (b *fsBackend) Stop() {
	fsMu.Lock()
	delete(fsRegistry, b.handle)
	fsMu.Unlock()
	if b.stream != nil {
		C.ccrec_stop(b.stream)
		b.stream = nil
	}
}

//export ccrecFSEventsCallback
func ccrecFSEventsCallback(h C.uintptr_t, n C.size_t, paths **C.char, flags *C.uint32_t, ids *C.uint64_t) {
	fsMu.Lock()
	b := fsRegistry[uintptr(h)]
	fsMu.Unlock()
	if b == nil {
		return
	}
	count := int(n)
	ps := unsafe.Slice(paths, count)
	fl := unsafe.Slice(flags, count)
	id := unsafe.Slice(ids, count)
	for i := 0; i < count; i++ {
		hint := Hint{Path: C.GoString(ps[i]), EventID: uint64(id[i])}
		if uint32(fl[i])&flagsNeedSweep != 0 {
			hint.MustScan = true
		}
		b.onHint(hint)
	}
}

//go:build linux

package watcher

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

// inotify has no history, so a restart always sweeps. It needs one watch per
// directory; see the README for the fs.inotify.max_user_watches limit.
const inotifyMask = syscall.IN_CREATE | syscall.IN_MODIFY | syscall.IN_CLOSE_WRITE |
	syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO | syscall.IN_DELETE |
	syscall.IN_ATTRIB | syscall.IN_DONT_FOLLOW

type inotifyBackend struct {
	mu     sync.Mutex
	fd     int
	file   *os.File
	wds    map[int32]string
	closed bool
	onHint func(Hint)
	done   chan struct{}
}

func newBackend() backend { return &inotifyBackend{} }

func (b *inotifyBackend) CanReplay() bool          { return false }
func (b *inotifyBackend) VolumeUUID(string) string { return "" }

func (b *inotifyBackend) Start(roots []string, _ uint64, _ bool, onHint func(Hint)) error {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return err
	}
	b.fd = fd
	b.file = os.NewFile(uintptr(fd), "inotify")
	b.wds = map[int32]string{}
	b.onHint = onHint
	b.done = make(chan struct{})
	for _, r := range roots {
		if err := b.addTree(r, false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_ = b.file.Close()
			return err
		}
	}
	go b.readLoop()
	return nil
}

// addTree watches root and every directory below it. With emit, files already
// present are hinted too (a new directory may have been filled before its
// watch existed).
func (b *inotifyBackend) addTree(root string, emit bool) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.closed {
				return nil
			}
			wd, err := syscall.InotifyAddWatch(b.fd, path, inotifyMask)
			if err != nil {
				if errors.Is(err, syscall.ENOENT) {
					return nil
				}
				return err
			}
			b.wds[int32(wd)] = path
			return nil
		}
		if emit && d.Type().IsRegular() {
			b.onHint(Hint{Path: path})
		}
		return nil
	})
}

func (b *inotifyBackend) readLoop() {
	defer close(b.done)
	buf := make([]byte, 64*1024)
	for {
		n, err := b.file.Read(buf)
		if err != nil {
			return
		}
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
			name := ""
			if ev.Len > 0 {
				raw := buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+int(ev.Len)]
				for i, c := range raw {
					if c == 0 {
						raw = raw[:i]
						break
					}
				}
				name = string(raw)
			}
			off += syscall.SizeofInotifyEvent + int(ev.Len)
			b.dispatch(ev, name)
		}
	}
}

func (b *inotifyBackend) dispatch(ev *syscall.InotifyEvent, name string) {
	if ev.Mask&syscall.IN_Q_OVERFLOW != 0 {
		b.onHint(Hint{MustScan: true})
		return
	}
	b.mu.Lock()
	dir, ok := b.wds[ev.Wd]
	if ev.Mask&syscall.IN_IGNORED != 0 {
		delete(b.wds, ev.Wd)
	}
	b.mu.Unlock()
	if !ok || name == "" {
		return
	}
	path := filepath.Join(dir, name)
	if ev.Mask&syscall.IN_ISDIR != 0 && ev.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 {
		_ = b.addTree(path, true)
		return
	}
	b.onHint(Hint{Path: path})
}

func (b *inotifyBackend) Stop() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	if b.file != nil {
		_ = b.file.Close()
		<-b.done
	}
}

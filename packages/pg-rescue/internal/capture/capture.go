// Package capture streams a process's output into a file with a size cap that
// keeps the head and the tail. It is how pg-rescue records the command's
// output (output.log), a handler's stderr and a verify command's output
// without ever buffering them in memory: only a small rolling window of the
// most recent bytes is held, for the report's output_tail.
package capture

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// Default limits: a capture is capped at 32 MiB. Past the cap the first and
// last 16 MiB are kept, with a truncation marker between them.
const (
	DefaultHead   = 16 << 20
	DefaultTail   = 16 << 20
	DefaultWindow = 64 << 10
)

// Limits sizes a capture. Zero fields take the defaults.
type Limits struct {
	// Head is how many leading bytes are kept.
	Head int64
	// Tail is how many trailing bytes are kept once the capture is over the
	// cap (Head+Tail).
	Tail int64
	// Window is how many of the most recent bytes Window() can return.
	Window int
}

func (l Limits) withDefaults() Limits {
	if l.Head <= 0 {
		l.Head = DefaultHead
	}
	if l.Tail <= 0 {
		l.Tail = DefaultTail
	}
	if l.Window <= 0 {
		l.Window = DefaultWindow
	}
	return l
}

// File is a capped capture file. It is safe for concurrent writers (the
// command's stdout and stderr readers both write to one File, so the file
// holds the two streams in arrival order). Write never fails the caller: a
// disk error is remembered (Err) and the stream keeps draining, so the
// process writing into the pipe never blocks.
type File struct {
	lim  Limits
	path string

	mu       sync.Mutex
	f        *os.File
	headLen  int64
	ring     *os.File
	ringPos  int64 // next write offset in the ring
	ringLen  int64 // valid bytes in the ring (<= lim.Tail)
	total    int64
	window   []byte // the last up to lim.Window+1 bytes
	err      error
	finished bool
}

// Create opens path (created exclusively, mode 0600) as a capture file.
func Create(path string, lim Limits) (*File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &File{lim: lim.withDefaults(), path: path, f: f}, nil
}

// Path is the capture file's path.
func (c *File) Path() string { return c.path }

// Write records p. It always reports len(p) written and a nil error.
func (c *File) Write(p []byte) (int, error) {
	n := len(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return n, nil
	}
	c.total += int64(n)
	c.keepWindow(p)
	if room := c.lim.Head - c.headLen; room > 0 {
		k := min(int64(len(p)), room)
		c.write(c.f, p[:k], 0, false)
		c.headLen += k
		p = p[k:]
	}
	if len(p) > 0 {
		c.writeRing(p)
	}
	return n, nil
}

func (c *File) write(f *os.File, p []byte, off int64, at bool) {
	if c.err != nil || len(p) == 0 {
		return
	}
	var err error
	if at {
		_, err = f.WriteAt(p, off)
	} else {
		_, err = f.Write(p)
	}
	if err != nil {
		c.err = err
	}
}

func (c *File) keepWindow(p []byte) {
	limit := c.lim.Window + 1
	if len(p) >= limit {
		c.window = append(c.window[:0], p[len(p)-limit:]...)
		return
	}
	c.window = append(c.window, p...)
	if len(c.window) > 2*limit {
		c.window = append(c.window[:0], c.window[len(c.window)-limit:]...)
	}
}

// writeRing stores p in the circular tail file, opening it on first use.
func (c *File) writeRing(p []byte) {
	if c.ring == nil {
		r, err := os.OpenFile(c.path+".tail", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if c.err == nil {
				c.err = err
			}
			return
		}
		c.ring = r
	}
	if int64(len(p)) > c.lim.Tail {
		// Only the last Tail bytes of p can survive.
		p = p[int64(len(p))-c.lim.Tail:]
		c.ringPos, c.ringLen = 0, 0
	}
	for len(p) > 0 {
		k := min(int64(len(p)), c.lim.Tail-c.ringPos)
		c.write(c.ring, p[:k], c.ringPos, true)
		p = p[k:]
		c.ringPos = (c.ringPos + k) % c.lim.Tail
		c.ringLen = min(c.ringLen+k, c.lim.Tail)
	}
}

// Total is the number of bytes written so far, before any cap.
func (c *File) Total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// Truncated reports whether bytes were dropped from the middle.
func (c *File) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total > c.lim.Head+c.lim.Tail
}

// Err is the first I/O error met while writing, if any.
func (c *File) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Window returns up to n of the most recent bytes (n is clamped to the
// configured window). lineStart is true when the first returned byte begins a
// line: either nothing earlier was written, or the byte before it was '\n'.
func (c *File) Window(n int) (b []byte, lineStart bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n = min(n, c.lim.Window)
	w := c.window
	if len(w) > n+1 {
		w = w[len(w)-(n+1):]
	}
	if len(w) <= n {
		return append([]byte(nil), w...), c.total == int64(len(w))
	}
	return append([]byte(nil), w[1:]...), w[0] == '\n'
}

// Close finishes the file: when the capture went over the cap it appends the
// truncation marker and the kept tail, then removes the scratch ring. It
// returns the first I/O error of the capture's life.
func (c *File) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return c.err
	}
	c.finished = true
	if c.ring != nil && c.ringLen > 0 {
		if c.total > c.lim.Head+c.lim.Tail {
			omitted := c.total - c.lim.Head - c.ringLen
			c.write(c.f, []byte(fmt.Sprintf("\n[pg-rescue: output truncated: %d bytes omitted; kept the first %d and the last %d]\n",
				omitted, c.lim.Head, c.ringLen)), 0, false)
		}
		c.copyRing()
	}
	if c.ring != nil {
		c.ring.Close()
		_ = os.Remove(c.path + ".tail")
	}
	if err := c.f.Close(); err != nil && c.err == nil {
		c.err = err
	}
	return c.err
}

// copyRing appends the ring's contents to the file, oldest byte first.
func (c *File) copyRing() {
	start := int64(0)
	if c.ringLen == c.lim.Tail {
		start = c.ringPos // the ring has wrapped: the oldest byte is at the write position
	}
	first := min(c.ringLen, c.lim.Tail-start)
	for _, seg := range [][2]int64{{start, first}, {0, c.ringLen - first}} {
		if seg[1] <= 0 || c.err != nil {
			continue
		}
		if _, err := io.Copy(c.f, io.NewSectionReader(c.ring, seg[0], seg[1])); err != nil {
			c.err = err
		}
	}
}

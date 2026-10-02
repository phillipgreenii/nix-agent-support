package capture

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/phillipgreenii/pg-rescue/internal/testenv"
)

func TestMain(m *testing.M) { os.Exit(testenv.Run(m)) }

func newFile(t *testing.T, lim Limits) *File {
	t.Helper()
	c, err := Create(filepath.Join(t.TempDir(), "out.log"), lim)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func finish(t *testing.T, c *File) string {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Path() + ".tail"); err == nil {
		t.Error("the scratch ring file was left behind")
	}
	return string(b)
}

// model is the specification: up to head+tail bytes verbatim, otherwise the
// first head bytes, the marker, and the last tail bytes.
func model(data string, head, tail int) string {
	if len(data) <= head+tail {
		return data
	}
	omitted := len(data) - head - tail
	return data[:head] + fmt.Sprintf("\n[pg-rescue: output truncated: %d bytes omitted; kept the first %d and the last %d]\n", omitted, head, tail) + data[len(data)-tail:]
}

func TestCapMatchesTheModelAtEveryBoundary(t *testing.T) {
	const head, tail = 7, 5
	for n := 0; n <= 40; n++ {
		data := strings.Repeat("0123456789abcdefghijklmnopqrstuvwxyz", 2)[:n]
		// Feed it in every chunking pattern so writes straddling the head,
		// the ring end and the wrap point are all exercised.
		for chunk := 1; chunk <= 13; chunk++ {
			c := newFile(t, Limits{Head: head, Tail: tail})
			for i := 0; i < len(data); i += chunk {
				c.Write([]byte(data[i:min(i+chunk, len(data))]))
			}
			if got, want := finish(t, c), model(data, head, tail); got != want {
				t.Fatalf("n=%d chunk=%d:\n got %q\nwant %q", n, chunk, got, want)
			}
		}
	}
}

func TestCapMatchesTheModelForRandomWrites(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for round := 0; round < 200; round++ {
		head, tail := 1+rng.Intn(20), 1+rng.Intn(20)
		var data strings.Builder
		c := newFile(t, Limits{Head: int64(head), Tail: int64(tail)})
		for i := 0; i < rng.Intn(12); i++ {
			chunk := make([]byte, rng.Intn(60))
			for j := range chunk {
				chunk[j] = byte('a' + rng.Intn(26))
			}
			data.Write(chunk)
			n, err := c.Write(chunk)
			if n != len(chunk) || err != nil {
				t.Fatalf("Write = %d, %v", n, err)
			}
		}
		if got, want := finish(t, c), model(data.String(), head, tail); got != want {
			t.Fatalf("round %d head=%d tail=%d:\n got %q\nwant %q", round, head, tail, got, want)
		}
		if c.Total() != int64(data.Len()) || c.Truncated() != (data.Len() > head+tail) {
			t.Fatalf("total=%d truncated=%v for %d bytes", c.Total(), c.Truncated(), data.Len())
		}
	}
}

func TestDefaultLimitsAreThe32MiBCapKeepingBothEnds(t *testing.T) {
	l := Limits{}.withDefaults()
	if l.Head != 16<<20 || l.Tail != 16<<20 || l.Window != 64<<10 {
		t.Errorf("defaults = %+v", l)
	}
}

func TestWindow(t *testing.T) {
	c := newFile(t, Limits{Head: 4, Tail: 4, Window: 10})
	b, start := c.Window(10)
	if len(b) != 0 || !start {
		t.Errorf("empty capture: %q %v", b, start)
	}
	c.Write([]byte("abc\ndef"))
	if b, start := c.Window(10); string(b) != "abc\ndef" || !start {
		t.Errorf("small: %q %v", b, start)
	}
	// Past the window: the oldest bytes fall off, and the answer says whether
	// the first kept byte starts a line.
	c.Write([]byte("\nghijkl"))
	b, start = c.Window(10)
	if want := "abc\ndef\nghijkl"[4:]; string(b) != want {
		t.Errorf("window = %q; want %q", b, want)
	}
	if !start {
		t.Error("the byte before \"def\\nghijkl\" is a newline, so it starts a line")
	}
	b, start = c.Window(5)
	if string(b) != "hijkl" || start {
		t.Errorf("window(5) = %q start=%v", b, start)
	}
	c.Close()
}

func TestWindowClampsAndSurvivesHugeWrites(t *testing.T) {
	c := newFile(t, Limits{Head: 4, Tail: 4, Window: 8})
	c.Write([]byte(strings.Repeat("x", 1000)))
	b, start := c.Window(100) // clamped to the configured window
	if len(b) != 8 || start {
		t.Errorf("window = %q start=%v", b, start)
	}
	c.Close()
}

func TestWriteNeverFailsTheWriterEvenOnDiskErrors(t *testing.T) {
	c := newFile(t, Limits{Head: 4, Tail: 4})
	c.Write([]byte("abc"))
	c.f.Close() // simulate the disk going away under the capture
	if n, err := c.Write([]byte("defgh")); n != 5 || err != nil {
		t.Errorf("Write = %d, %v", n, err)
	}
	if c.Err() == nil {
		t.Error("the disk error was not remembered")
	}
	if err := c.Close(); err == nil {
		t.Error("Close must report the remembered error")
	}
	if n, err := c.Write([]byte("late")); n != 4 || err != nil {
		t.Errorf("a write after Close = %d, %v", n, err)
	}
}

func TestConcurrentWritersKeepWritesWhole(t *testing.T) {
	c := newFile(t, Limits{Head: 1 << 20, Tail: 1 << 20})
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			line := []byte(strings.Repeat(string(rune('a'+w)), 99) + "\n")
			for i := 0; i < 200; i++ {
				c.Write(line)
			}
		}()
	}
	wg.Wait()
	got := finish(t, c)
	for _, l := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if len(l) != 99 || strings.Trim(l, l[:1]) != "" {
			t.Fatalf("interleaved line %q", l)
		}
	}
}

func TestCreateIsExclusiveAndPrivate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x")
	c, err := Create(p, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if _, err := Create(p, Limits{}); err == nil {
		t.Error("Create must not reuse an existing file")
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), p+".link"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(p+".link", Limits{}); err == nil {
		t.Error("Create must not follow a symlink")
	}
}

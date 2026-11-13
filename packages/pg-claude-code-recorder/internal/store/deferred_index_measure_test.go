//go:build soak

package store

// Opt-in measurement (V-4: not a unit test, soak tag only): the bytes a bulk
// load writes with the secondary indexes maintained row by row against the same
// load with them built after it. The page cache is shrunk so a modest synthetic
// load behaves like a corpus larger than the real 256 MiB cache. Run:
//
//	PG_CCREC_INDEX_ROWS=1500000 go test -tags soak -run TestMeasureDeferredIndexes -v ./internal/store/
//
// Bytes written are the process's disk writes, taken after a sync (Linux /proc/self/io, or darwin
// proc_pid_rusage through python3 ctypes); with synchronous=OFF the number is a
// lower bound and directional only.

import (
	"context"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writtenBytes() int64 {
	if b, err := os.ReadFile("/proc/self/io"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "write_bytes: "); ok {
				n, _ := strconv.ParseInt(v, 10, 64)
				return n
			}
		}
	}
	const py = `import ctypes,sys
class R(ctypes.Structure):
    _fields_=[("uuid",ctypes.c_ubyte*16)]+[(n,ctypes.c_uint64) for n in "user sys idle_wake int_wake pgins wired resident phys_footprint proc_start abstime_ignored child_user child_sys child_idle child_int child_pgins child_ignored diskio_read diskio_write".split()]
r=R();ctypes.CDLL("/usr/lib/libproc.dylib").proc_pid_rusage(int(sys.argv[1]),2,ctypes.byref(r));print(r.diskio_write)`
	out, err := exec.CommandContext(context.Background(), "python3", "-I", "-c", py, strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return n
}

// batchRows is the rows per commit, a scaled-down bulk batch budget.
const batchRows = 20000

func TestMeasureDeferredIndexes(t *testing.T) {
	rows, _ := strconv.Atoi(os.Getenv("PG_CCREC_INDEX_ROWS"))
	if rows <= 0 {
		t.Skip("PG_CCREC_INDEX_ROWS not set")
	}
	run := func(name string, inline bool) int64 {
		dir := seededDir(t)
		s, err := Open(dir, Options{FreeBytes: ampleFreeBytes, Rebuild: true, BulkRebuild: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.wdb.ExecContext(context.Background(), `PRAGMA cache_size=-4096`); err != nil {
			t.Fatal(err)
		}
		if inline {
			if err := s.EnsureIndexes(); err != nil {
				t.Fatal(err)
			}
		}
		rng := rand.New(rand.NewSource(1))
		b0, t0 := writtenBytes(), time.Now()
		tx, err := s.wdb.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < rows; i++ {
			res, err := tx.ExecContext(context.Background(), `INSERT INTO records(file_id,gen,off,item,len,source,type,parser_version,session,time_ns) VALUES(?,1,?,0,10,'s',?,1,?,?)`,
				i%500, i, "t"+strconv.Itoa(rng.Intn(8)), "sess-"+strconv.Itoa(rng.Intn(5000)), rng.Int63())
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res.LastInsertId()
			if _, err := tx.ExecContext(context.Background(), `INSERT INTO fields(rec_id,name_id,s,n) VALUES(?,?,NULL,?)`, id, 1+rng.Intn(3), rng.Int63n(1<<40)); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(context.Background(), `INSERT INTO texts(rec_id,field,kind) VALUES(?,'body','text')`, id); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRebuild(); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		n := writtenBytes() - b0
		t.Logf("%s: %d records, %d bytes written, %s", name, rows, n, time.Since(t0).Round(time.Millisecond))
		return n
	}
	inline := run("inline indexes", true)
	deferred := run("deferred indexes", false)
	if deferred > 0 {
		t.Logf("reduction: %.1fx fewer bytes written (%d / %d)", float64(inline)/float64(deferred), inline, deferred)
	}
}

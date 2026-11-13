//go:build soak

package parser_test

// Opt-in measurement (V-4: not a unit test, soak tag only, and not named
// TestSoak*): the cost of a default build against a bulk rebuild over a
// synthetic corpus. Run:
//
//	PG_CCREC_BULK_FILES=600 go test -tags soak -run TestMeasureBulkRebuild -v ./internal/parser/
//
// Block output (getrusage ru_oublock) counts blocks the process caused to be
// written; with synchronous=OFF the kernel may flush later, so the bulk number
// is a lower bound. Wall time is directional only.

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// oublock returns the bytes this process has written to disk: Linux
// /proc/self/io write_bytes, or on darwin libproc proc_pid_rusage read through
// python3 ctypes. It is 0 when neither is available.
func oublock() int64 {
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

func TestMeasureBulkRebuild(t *testing.T) {
	files, _ := strconv.Atoi(os.Getenv("PG_CCREC_BULK_FILES"))
	if files <= 0 {
		t.Skip("PG_CCREC_BULK_FILES not set")
	}
	run := func(name string, opt *store.Options) {
		k := bulkCorpus(t, files)
		if opt != nil {
			k.StoreOptions = *opt
			k.Reopen()
		}
		b0, t0 := oublock(), time.Now()
		k.Parse()
		if opt != nil {
			if err := k.Store.FinishRebuild(); err != nil {
				t.Fatal(err)
			}
		}
		recs := k.Count(`SELECT COUNT(*) FROM records`)
		commits := k.Count(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='parse_seq'`)
		if opt != nil {
			commits = k.Store.BulkCommits()
		}
		t.Logf("%s: %d records, %d commits (%.4f/record), %d bytes written (%.0f per record), %s",
			name, recs, commits, float64(commits)/float64(recs), oublock()-b0, float64(oublock()-b0)/float64(recs), time.Since(t0).Round(time.Millisecond))
	}
	run("default", nil)
	run("bulk", &store.Options{Rebuild: true, BulkRebuild: true})
}

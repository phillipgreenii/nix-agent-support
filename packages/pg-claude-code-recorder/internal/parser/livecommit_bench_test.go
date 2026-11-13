//go:build soak

package parser_test

// Benchmark of the live commit path: files grow by a few lines per round and
// the parser catches up, either one wake-up at a time (the old per-file
// transaction per wake-up) or once per round (a coalesced wake-up). It is a Go
// benchmark, so `go test` never runs it; run it by hand:
//
//	go test -tags soak -run '^$' -bench BenchmarkLiveCommit -benchtime 1x ./internal/parser/
//
// The capture-only mode appends without parsing, so its bytes are the capture
// side alone; subtract them from the other modes to get the parse side.
//
// Reported per run: wall time, parse transactions (store write commits),
// capture commits (journal plus database), bytes written (process disk-write
// counter; darwin reads libproc through python3, linux /proc/self/io, else 0).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/testkit"
)

func diskWriteBytes() int64 {
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

func BenchmarkLiveCommit(b *testing.B) {
	const files, rounds = 20, 40
	line := func(f, r, i int) string {
		return fmt.Sprintf(`{"type":"assistant","uuid":"u%d-%d-%d","sessionId":"example-session-%d","timestamp":"2026-10-29T20:00:00Z","message":{"id":"m%d-%d-%d","model":"example-model-a","role":"assistant","usage":{"output_tokens":5},"content":[{"type":"text","text":"some words %d"}]}}`+"\n", f, r, i, f, f, r, i, r)
	}
	for _, mode := range []string{"capture-only", "per-wakeup", "coalesced"} {
		b.Run(mode, func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				k := testkit.New(b)
				ids := make([]int64, files)
				for f := range ids {
					ids[f] = k.Add("example-proj", fmt.Sprintf("example-session-%d.jsonl", f), line(f, 0, 0))
				}
				k.Parse()
				var wr, cm atomic.Int64
				k.Store.OnWrite(func() { wr.Add(1) })
				k.Store.OnCommit(func() { cm.Add(1) })
				var parseTime time.Duration
				b0, t0 := diskWriteBytes(), time.Now()
				for r := 1; r <= rounds; r++ {
					for f, id := range ids {
						k.Append(id, line(f, r, 1)+line(f, r, 2))
						k.Parser.Notify(id)
						if mode == "per-wakeup" {
							d0 := time.Now()
							if err := k.Parser.Drain(); err != nil {
								b.Fatal(err)
							}
							parseTime += time.Since(d0)
						}
					}
					if mode == "coalesced" {
						d0 := time.Now()
						if err := k.Parser.Drain(); err != nil {
							b.Fatal(err)
						}
						parseTime += time.Since(d0)
					}
				}
				el := time.Since(t0)
				written := diskWriteBytes() - b0
				recs := k.Count(`SELECT COUNT(*) FROM records`)
				if want := files * (1 + rounds*2); mode != "capture-only" && recs < want {
					b.Fatalf("records = %d, want at least %d", recs, want)
				}
				b.ReportMetric(float64(wr.Load()), "parse_tx")
				b.ReportMetric(float64(cm.Load()), "capture_commits")
				b.ReportMetric(float64(wr.Load()+cm.Load())/el.Seconds(), "commits/s")
				if wr.Load() > 0 {
					b.ReportMetric(float64(wr.Load())/parseTime.Seconds(), "parse_commits/s")
					b.ReportMetric(float64(parseTime.Milliseconds()), "parse_ms")
				}
				b.ReportMetric(float64(written), "bytes_written")
			}
		})
	}
}

package serve

import (
	"os"
	"runtime/debug"
	"runtime/metrics"
	"syscall"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// Runtime samples are read through one pre-built slice: metrics.Read does not
// stop the world, unlike runtime.ReadMemStats.
const (
	rtGoroutines = "/sched/goroutines:goroutines"
	rtHeapBytes  = "/memory/classes/heap/objects:bytes"
	rtAllocBytes = "/gc/heap/allocs:bytes"
)

// collectRuntimeMetrics records the process CPU time, the Go runtime figures
// and the size of the SQLite WAL file at walPath (0 when it does not exist).
// It runs on every scrape and on the refresh loop, reads each source into
// locals and then writes the registry; it never touches the database.
func collectRuntimeMetrics(reg *obs.Registry, walPath string) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err == nil {
		cpu := float64(ru.Utime.Nano()+ru.Stime.Nano()) / 1e9
		reg.SetCounter(obs.MProcessCPUSeconds, nil, cpu)
	}

	samples := []metrics.Sample{{Name: rtGoroutines}, {Name: rtHeapBytes}, {Name: rtAllocBytes}}
	metrics.Read(samples)
	if samples[0].Value.Kind() == metrics.KindUint64 {
		reg.Set(obs.MGoGoroutines, nil, float64(samples[0].Value.Uint64()))
	}
	if samples[1].Value.Kind() == metrics.KindUint64 {
		reg.Set(obs.MGoHeapAlloc, nil, float64(samples[1].Value.Uint64()))
	}
	if samples[2].Value.Kind() == metrics.KindUint64 {
		reg.SetCounter(obs.MGoAllocTotal, nil, float64(samples[2].Value.Uint64()))
	}

	var gc debug.GCStats
	debug.ReadGCStats(&gc)
	reg.SetCounter(obs.MGoGCPauseSeconds, nil, gc.PauseTotal.Seconds())

	var wal float64
	if fi, err := os.Stat(walPath); err == nil {
		wal = float64(fi.Size())
	}
	reg.Set(obs.MWALBytes, nil, wal)
}

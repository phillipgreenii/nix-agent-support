// Package metrics holds the pg-checkout-pool metric set. The pool is a set of
// short CLI invocations with no daemon, so event metrics (resets, rescues,
// acquire waits) are accumulated in a small journal file under the pool root
// and the gauges are computed from the slot store at render time. Render turns
// both into Prometheus text exposition, which `pg-checkout-pool metrics`
// prints or writes atomically for a textfile collector.
package metrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

const (
	journalFile = "metrics.json"
	lockFile    = "metrics.lock"
	waitersDir  = "waiters"
)

// ResetBuckets are the upper bounds, in seconds, of the reset duration
// histogram. A measured ZR reset takes about 9 s (6.5 to 14 s under load), so
// the bounds are dense between 5 and 60 s.
var ResetBuckets = []float64{1, 2, 5, 10, 15, 20, 30, 45, 60, 120, 300}

// WaitBuckets are the upper bounds, in seconds, of the acquire wait histogram.
// A lease waits up to two minutes by default.
var WaitBuckets = []float64{0.1, 1, 5, 15, 30, 60, 120, 300, 900}

// Hist is a fixed-bucket histogram. Counts[i] counts observations at most
// Bounds[i] and above Bounds[i-1]; Overflow counts the rest.
type Hist struct {
	Counts   []uint64 `json:"counts"`
	Overflow uint64   `json:"overflow"`
	Sum      float64  `json:"sum"`
	Count    uint64   `json:"count"`
}

// Observe adds one observation against bounds.
func (h *Hist) Observe(bounds []float64, v float64) {
	if len(h.Counts) != len(bounds) {
		h.Counts = make([]uint64, len(bounds))
	}
	h.Sum += v
	h.Count++
	for i, b := range bounds {
		if v <= b {
			h.Counts[i]++
			return
		}
	}
	h.Overflow++
}

// Journal is the persisted event state.
type Journal struct {
	Schema        int    `json:"schema"`
	Reset         Hist   `json:"reset"`
	AcquireWait   Hist   `json:"acquire_wait"`
	ResetFailures uint64 `json:"reset_failures"`
	Rescued       uint64 `json:"rescued"`
}

// Load reads the journal under root. A missing journal is an empty one.
func Load(root string) (Journal, error) {
	var j Journal
	b, err := os.ReadFile(filepath.Join(root, journalFile))
	if errors.Is(err, os.ErrNotExist) {
		return Journal{Schema: 1}, nil
	}
	if err != nil {
		return j, err
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return Journal{Schema: 1}, fmt.Errorf("metrics journal: %w", err)
	}
	return j, nil
}

// Update applies fn to the journal under an exclusive lock and writes it back
// atomically. A corrupt journal is replaced by an empty one rather than
// blocking the pool.
func Update(root string, fn func(*Journal)) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(filepath.Join(root, lockFile), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = lf.Close() }()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }()
	j, _ := Load(root)
	j.Schema = 1
	fn(&j)
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, journalFile+".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(root, journalFile))
}

// Waiting records that this process is waiting for a free slot and returns
// the function that clears the record. A crashed waiter leaves a marker whose
// pid is dead, which CountWaiters ignores and removes.
func Waiting(root string, pid int) (done func(), err error) {
	dir := filepath.Join(root, waitersDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}, err
	}
	p := filepath.Join(dir, strconv.Itoa(pid))
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		return func() {}, err
	}
	return func() { _ = os.Remove(p) }, nil
}

// CountWaiters counts the waiter markers whose process is alive.
func CountWaiters(root string) int {
	dir := filepath.Join(root, waitersDir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		n++
	}
	return n
}

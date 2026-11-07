package metrics

import (
	"fmt"
	"strconv"
	"strings"
)

// Prefix is the metric namespace.
const Prefix = "pg_checkout_pool_"

// States are the slot states a gauge series is always exported for, so a
// state with no slots reads 0 instead of vanishing.
var States = []string{"warm-free", "leased", "resetting", "quarantined", "retired"}

// Gauges are the point-in-time values computed from the slot store.
type Gauges struct {
	SlotsByState map[string]int
	LeaseAgeMax  float64
	Leaked       int
	Quarantined  int
	Waiters      int
	// FreeBytes and Daemons are nil when their probe failed: the series is
	// then omitted rather than exported as a misleading 0.
	FreeBytes *uint64
	Daemons   *int
}

func num(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func writeHist(b *strings.Builder, name, help string, bounds []float64, h Hist) {
	fmt.Fprintf(b, "# HELP %s%s %s\n# TYPE %s%s histogram\n", Prefix, name, help, Prefix, name)
	var cum uint64
	for i, ub := range bounds {
		if i < len(h.Counts) {
			cum += h.Counts[i]
		}
		fmt.Fprintf(b, "%s%s_bucket{le=%q} %d\n", Prefix, name, num(ub), cum)
	}
	fmt.Fprintf(b, "%s%s_bucket{le=\"+Inf\"} %d\n", Prefix, name, h.Count)
	fmt.Fprintf(b, "%s%s_sum %s\n%s%s_count %d\n", Prefix, name, num(h.Sum), Prefix, name, h.Count)
}

func gauge(b *strings.Builder, name, help, typ, val string) {
	fmt.Fprintf(b, "# HELP %s%s %s\n# TYPE %s%s %s\n%s%s %s\n", Prefix, name, help, Prefix, name, typ, Prefix, name, val)
}

// Render returns the Prometheus text exposition of g and j.
func Render(g Gauges, j Journal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP %sslots Slots by lifecycle state.\n# TYPE %sslots gauge\n", Prefix, Prefix)
	for _, s := range States {
		fmt.Fprintf(&b, "%sslots{state=%q} %d\n", Prefix, s, g.SlotsByState[s])
	}
	gauge(&b, "lease_age_seconds", "Age of the oldest active lease, 0 when none.", "gauge", num(g.LeaseAgeMax))
	gauge(&b, "leaked_slots", "Leased or resetting slots whose lock is free and whose expiry has passed.", "gauge", strconv.Itoa(g.Leaked))
	gauge(&b, "quarantined_slots", "Slots taken out of rotation with their directory kept.", "gauge", strconv.Itoa(g.Quarantined))
	gauge(&b, "waiters", "Lease commands currently waiting for a free slot.", "gauge", strconv.Itoa(g.Waiters))
	if g.FreeBytes != nil {
		gauge(&b, "free_bytes", "Free bytes on the volume holding the pool root.", "gauge", strconv.FormatUint(*g.FreeBytes, 10))
	}
	if g.Daemons != nil {
		gauge(&b, "fsmonitor_daemons", "Git fsmonitor daemons whose working directory is inside a slot.", "gauge", strconv.Itoa(*g.Daemons))
	}
	writeHist(&b, "acquire_wait_seconds", "Time a lease command spent obtaining a slot.", WaitBuckets, j.AcquireWait)
	writeHist(&b, "reset_seconds", "Time a slot reset took.", ResetBuckets, j.Reset)
	gauge(&b, "reset_failures_total", "Slot resets that failed twice and quarantined the slot.", "counter", strconv.FormatUint(j.ResetFailures, 10))
	gauge(&b, "rescued_total", "Releases that captured uncommitted tracked work to a rescue ref.", "counter", strconv.FormatUint(j.Rescued, 10))
	return b.String()
}

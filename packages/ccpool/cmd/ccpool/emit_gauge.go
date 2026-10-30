package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/phillipgreenii/ccpool/internal/telemetry"
)

// gaugeRecorder records one gauge point: telemetry.RecordGauge in
// production, a spy in tests.
type gaugeRecorder func(name string, value float64, labels map[string]string) error

// labelFlags collects repeated --label key=value flags.
type labelFlags map[string]string

func (l labelFlags) String() string { return fmt.Sprint(map[string]string(l)) }

func (l labelFlags) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("label %q must be key=value", s)
	}
	l[k] = v
	return nil
}

// runEmitGauge is `ccpool emit-gauge --name N [--label k=v]... --value V`: a
// generic, handler-agnostic gauge emitter for short-lived callers (the
// pg-router ccpool handler execs it as it execs `capacity`). main()'s
// telemetry.Init/shutdown flushes the point on exit; with no
// OTEL_EXPORTER_OTLP_ENDPOINT the provider is a no-op and this is a silent
// exit-0 (bead pg2-fx5x0l.8).
func runEmitGauge(args []string) int {
	return emitGauge(args, telemetry.RecordGauge, os.Stderr)
}

func emitGauge(args []string, record gaugeRecorder, stderr *os.File) int {
	fs := flag.NewFlagSet("emit-gauge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "metric name (Prometheus identifier)")
	value := fs.String("value", "", "gauge value (float)")
	labels := labelFlags{}
	fs.Var(labels, "label", "label key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" || *value == "" {
		fmt.Fprintln(stderr, "emit-gauge: --name and --value are required")
		return 2
	}
	v, err := strconv.ParseFloat(*value, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		fmt.Fprintf(stderr, "emit-gauge: invalid --value %q\n", *value)
		return 2
	}
	if err := record(*name, v, labels); err != nil {
		fmt.Fprintf(stderr, "emit-gauge: %v\n", err)
		return 2
	}
	return 0
}

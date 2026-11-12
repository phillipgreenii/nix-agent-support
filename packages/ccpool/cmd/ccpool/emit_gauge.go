package main

import (
	"encoding/json"
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

// gaugePoint is the JSON shape of one --point: a batch of points shares one
// process, one exporter dial and one flush.
type gaugePoint struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}

// pointFlags collects repeated --point JSON flags.
type pointFlags []gaugePoint

func (p *pointFlags) String() string { return fmt.Sprint([]gaugePoint(*p)) }

func (p *pointFlags) Set(s string) error {
	var pt gaugePoint
	if err := json.Unmarshal([]byte(s), &pt); err != nil {
		return fmt.Errorf("point %q: %v", s, err)
	}
	if pt.Name == "" || math.IsNaN(pt.Value) || math.IsInf(pt.Value, 0) {
		return fmt.Errorf("point %q needs a name and a finite value", s)
	}
	*p = append(*p, pt)
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
	var points pointFlags
	fs.Var(&points, "point", `batch point as JSON {"name":N,"value":V,"labels":{k:v}} (repeatable; replaces --name/--value)`)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(points) > 0 {
		if *name != "" || *value != "" {
			fmt.Fprintln(stderr, "emit-gauge: --point cannot be combined with --name/--value")
			return 2
		}
		for _, pt := range points {
			if err := record(pt.Name, pt.Value, pt.Labels); err != nil {
				fmt.Fprintf(stderr, "emit-gauge: %v\n", err)
				return 2
			}
		}
		return 0
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

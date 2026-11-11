// Package sensorcli is the subcommand dispatch skeleton of the sensors binary:
//
//	pg-router-sensors <subcommand> [--config FILE] [--state-dir DIR] [--resource RESOURCE] [--raw] [--bands]
//
// One invocation answers all the resources of that sensor, one JSONL line per
// resource on stdout, diagnostics on stderr. The state machine, the answer
// printer and the state file come from the sibling library packages; a sensor
// supplies only its probes, its band tables and its configuration keys.
//
// Exit status: 0 when at least one resource was answered (or --bands printed);
// 1 when none could be measured (a pull failure for the router); 2 for a usage
// or configuration error.
package sensorcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-sensors/internal/answer"
	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
)

// Exit codes.
const (
	ExitOK     = 0
	ExitNone   = 1
	ExitUsage  = 2
	refreshEnv = "PG_ROUTER_LEVEL_REFRESH"
)

// Env is what a sensor's Measure receives.
type Env struct {
	Now time.Time
	// Raw is set under --raw: no state is read or written, so a sensor MUST NOT
	// use its failure counters.
	Raw bool
	// Refresh is set when the router started this pull for `level refresh`
	// (PG_ROUTER_LEVEL_REFRESH=1); a credential sensor then skips the rise
	// dwell via Observation.BypassDwell.
	Refresh bool
	// Resource limits the run to one resource ("" means all).
	Resource string
	// Store gives access to the generic extra-state slot (Extra/SetExtra). It
	// is an in-memory store under --raw.
	Store  *levels.Store
	Getenv func(string) string
	Stderr io.Writer
}

// Sensor is one subcommand's probes.
type Sensor interface {
	// Resources lists the resources the sensor answers, for --resource and
	// --bands.
	Resources() []string
	// Bands returns the band tables of the sensor (an empty list for a
	// two-level scale), one record per resource that has one.
	Bands() []levels.Info
	// Configure applies the sensor's configuration (the table left after the
	// common keys); an error makes the run exit 2.
	Configure(cfg Config) error
	// Measure takes the readings. It returns an observation per resource it
	// could read and omits the rest; an error means nothing could be measured.
	Measure(ctx context.Context, env Env) ([]levels.Observation, error)
}

// Registry maps a subcommand name to its sensor. A subcommand whose packet has
// not landed is simply not registered.
type Registry map[string]Sensor

// IO carries the process surroundings, so Run is testable.
type IO struct {
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	Now    func() time.Time
}

// Usage prints the usage text.
func Usage(w io.Writer, reg Registry) {
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(w, "usage: pg-router-sensors <subcommand> [--config FILE] [--state-dir DIR] [--resource RESOURCE] [--raw] [--bands]\n")
	fmt.Fprintf(w, "subcommands: %s\n", strings.Join(names, ", "))
}

// Run executes the binary and returns the process exit status.
func Run(args []string, reg Registry, sio IO) int {
	if sio.Getenv == nil {
		sio.Getenv = os.Getenv
	}
	if sio.Now == nil {
		sio.Now = time.Now
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		Usage(sio.Stderr, reg)
		return ExitUsage
	}
	name := args[0]
	sensor, ok := reg[name]
	if !ok {
		fmt.Fprintf(sio.Stderr, "pg-router-sensors: unknown subcommand %q\n", name)
		Usage(sio.Stderr, reg)
		return ExitUsage
	}

	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(sio.Stderr)
	configPath := fs.String("config", "", "TOML configuration file (absent: built-in defaults)")
	stateDir := fs.String("state-dir", "", "state directory (default $XDG_STATE_HOME/pg-router-sensors)")
	only := fs.String("resource", "", "answer only this resource")
	raw := fs.Bool("raw", false, "read and write no state; no dwell, no hysteresis")
	bands := fs.Bool("bands", false, "print the band tables and exit")
	if err := fs.Parse(args[1:]); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(sio.Stderr, "pg-router-sensors %s: unexpected argument %q\n", name, fs.Arg(0))
		return ExitUsage
	}

	cfg, dwell, validFor, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(sio.Stderr, "pg-router-sensors %s: %v\n", name, err)
		return ExitUsage
	}
	if err := sensor.Configure(cfg); err != nil {
		fmt.Fprintf(sio.Stderr, "pg-router-sensors %s: %v\n", name, err)
		return ExitUsage
	}
	if *only != "" && !contains(sensor.Resources(), *only) {
		fmt.Fprintf(sio.Stderr, "pg-router-sensors %s: unknown resource %q (have: %s)\n", name, *only, strings.Join(sensor.Resources(), ", "))
		return ExitUsage
	}

	if *bands {
		return printBands(sio.Stdout, sio.Stderr, sensor, *only)
	}

	var store *levels.Store
	if *raw {
		store = levels.NewMemoryStore()
	} else {
		dir := *stateDir
		if dir == "" {
			dir = defaultStateDir(sio.Getenv)
		}
		store = levels.OpenStore(levels.StatePath(dir, name))
		if store.LoadErr != nil {
			fmt.Fprintf(sio.Stderr, "pg-router-sensors %s: state lost, restarting at the worst level: %v\n", name, store.LoadErr)
		}
	}

	now := sio.Now()
	obs, err := sensor.Measure(context.Background(), Env{
		Now:      now,
		Raw:      *raw,
		Refresh:  sio.Getenv(refreshEnv) == "1",
		Resource: *only,
		Store:    store,
		Getenv:   sio.Getenv,
		Stderr:   sio.Stderr,
	})
	if err != nil {
		fmt.Fprintf(sio.Stderr, "pg-router-sensors %s: %v\n", name, err)
		return ExitNone
	}

	printer := answer.Printer{W: sio.Stdout}
	answered := 0
	for _, o := range obs {
		if *only != "" && o.Resource != *only {
			continue
		}
		level := o.Level
		if !*raw {
			level, _ = store.Apply(o, now, dwell, false)
		} else if o.Scale.Index(o.Level) < 0 {
			fmt.Fprintf(sio.Stderr, "pg-router-sensors %s: resource %q: level %q is not in its scale\n", name, o.Resource, o.Level)
			continue
		}
		observed := o.ObservedAt
		if observed.IsZero() {
			observed = now
		}
		a := answer.Answer{Resource: o.Resource, Level: level, ObservedAt: observed, ValidForS: validFor, Owner: name}
		if o.Reading != nil {
			a.Reading = &answer.Reading{Value: o.Reading.Value, Unit: o.Reading.Unit}
		}
		if err := printer.Print(a); err != nil {
			fmt.Fprintf(sio.Stderr, "pg-router-sensors %s: %v\n", name, err)
			continue
		}
		answered++
	}
	if !*raw {
		if err := store.Save(); err != nil {
			fmt.Fprintf(sio.Stderr, "pg-router-sensors %s: state not saved: %v\n", name, err)
		}
	}
	if answered == 0 && !emptyOK(sensor) {
		fmt.Fprintf(sio.Stderr, "pg-router-sensors %s: no resource could be answered\n", name)
		return ExitNone
	}
	return ExitOK
}

// EmptyOK is an optional interface of a sensor whose successful measurement
// with nothing to answer exits 0 with no lines (claude-sensor); a measurement
// error still exits 1.
type EmptyOK interface{ EmptyOK() bool }

func emptyOK(s Sensor) bool {
	e, ok := s.(EmptyOK)
	return ok && e.EmptyOK()
}

func printBands(stdout, stderr io.Writer, s Sensor, only string) int {
	enc := json.NewEncoder(stdout)
	for _, info := range s.Bands() {
		if only != "" && info.Resource != only {
			continue
		}
		if info.Bands == nil {
			info.Bands = []levels.BandInfo{}
		}
		if err := enc.Encode(info); err != nil {
			fmt.Fprintf(stderr, "pg-router-sensors: %v\n", err)
			return ExitNone
		}
	}
	return ExitOK
}

func defaultStateDir(getenv func(string) string) string {
	if x := getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "pg-router-sensors")
	}
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".local", "state", "pg-router-sensors")
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// Package policy is the battery rule shared by the pg-keep-awake keeper and
// anchor: the reading of the Mac's power source, the hysteresis gate that turns
// readings into one of five gate names, and the blind rule that stops holding
// the assertion when the battery cannot be read on a low charge.
//
// # Origin
//
// The gate, the hysteresis latch and the pmset parsing are ported from
// pa-monitor's internal/core/caffeinate/battery.go and
// internal/core/power (a separate Go module, so they cannot be imported). The
// gate names match pa-monitor's, so pa-monitor can read them back unchanged.
//
// # Reading
//
// A Reader yields one Reading per call. The production Reader (PmsetReader)
// runs `pmset -g batt` by the absolute path /usr/bin/pmset under a timeout; it
// does not use IOKit (the design allows an in-process IOKit read as a later
// replacement, which would avoid a fork per tick). The hidden environment
// variable BatterySourceEnv (PG_KEEP_AWAKE_BATTERY_SOURCE=file:<path>) swaps
// the reader for FileReader so tests can drive the battery; it is honored in
// every build.
//
// # Gate
//
// An Evaluator holds the in-memory hysteresis latch, the consecutive
// read-failure counter and the last good reading. The keeper reads the battery
// once per tick and feeds the outcome to Evaluator.Observe, sharing the result
// across its checks. The anchor has no hold level of its own, no hysteresis and
// no linger, so it calls only Blind.
package policy

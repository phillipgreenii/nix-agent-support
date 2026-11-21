// Package client is the published contract of the pg-connector umbrella CLI:
// the result types its verbs print, the exit-code constants, a typed error, a
// Client that runs one verb per method through a cmdrun.Runner, and a test
// fake.
//
// The umbrella (cmd/pg-connector) encodes with these same types, so one
// definition serves both the producer and every consumer.
//
// Closure rule: this package MUST depend on the standard library, x/cmdrun
// and x/exitcode only (x/exitcode is needed because the exit codes are
// re-exports of the shared workspace codes, not new definitions). A closure
// test fails on any other dependency. The types, constants and codec live
// here; no engine type, no logging, no I/O beyond the Runner.
//
// Wire evolution is additive only: a field added to an existing envelope is
// optional, and an absent schemaVersion reads as version 1.
package client

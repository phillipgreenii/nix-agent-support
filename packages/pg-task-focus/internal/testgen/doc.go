// Package testgen holds the pgregory.net/rapid generators for property tests
// (event logs valid by construction, instants and zone names) and the
// deterministic scripted-day builder shared by the pg-task-focus test suites
// and benchmarks. It imports only event and rapid, so the projection tests
// can use it from their external test package.
package testgen

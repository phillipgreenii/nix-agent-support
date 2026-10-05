// Package contract holds the build-tagged (contract) suite that pins the real
// bd command line surface the exporter depends on. It is deliberately not part
// of the default test run: it needs a real bd binary and creates throwaway
// embedded databases. Run it with the beads-exporter-contract runner, or
// directly:
//
//	go test -tags contract ./internal/contract -bd /path/to/bd [-update]
//
// With -update it records the testdata/bd fixtures and the VERSION pin.
package contract

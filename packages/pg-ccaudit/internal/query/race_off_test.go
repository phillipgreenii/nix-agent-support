//go:build !race

package query

// raceBuild is false without the race detector; see race_on_test.go.
const raceBuild = false

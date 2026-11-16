//go:build race

package query

// raceBuild is true under the race detector, which makes the SQLite-heavy
// window-driver equality test (it runs the old quadratic statements as its
// oracle) 20 to 30 times slower; the fixture shrinks to keep the unit gate
// well inside its package timeout instead of raising the timeout.
const raceBuild = true

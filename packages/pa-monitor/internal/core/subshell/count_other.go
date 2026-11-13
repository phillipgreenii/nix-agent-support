//go:build !darwin

package subshell

// defaultCount keeps the pgrep -P -l path on non-darwin platforms.
func defaultCount(parent int) (int, error) {
	out, err := DefaultRunPs(parent)
	if err != nil {
		return 0, err
	}
	return CountPgrepOutput(out), nil
}

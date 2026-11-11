package settle

// removalCap says whether the next removal candidate is skipped for a rate
// limit, and which block reason applies ("" for none). The per-run cap is
// checked first. hourCount is a function because the shell reads it lazily:
// it prunes and locks, and the per-run cap makes the read unnecessary.
func removalCap(runCount, maxPerRun int, hourCount func() int, maxPerHour int) string {
	switch {
	case runCount >= maxPerRun:
		return BlockRunCap
	case hourCount() >= maxPerHour:
		return BlockHourCap
	}
	return ""
}

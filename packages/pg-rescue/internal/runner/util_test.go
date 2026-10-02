package runner

import "time"

func parseDur(s string) (time.Duration, error) { return time.ParseDuration(s) }

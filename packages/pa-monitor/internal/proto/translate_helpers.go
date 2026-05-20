package proto

import (
	"fmt"
	"time"
)

func parseMonday(period string) (time.Time, error) {
	return time.Parse("2006-01-02", period)
}

func formatISOWeek(year, week int) string {
	return fmt.Sprintf("%d-W%02d", year, week)
}

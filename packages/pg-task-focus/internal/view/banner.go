package view

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/projection"

// RolloverBanner is the text shown when a period of kind k has ended and the
// operator has not rolled it over. An unknown kind has no banner.
func RolloverBanner(k projection.Kind) string {
	switch k {
	case projection.Day:
		return "New day: roll over"
	case projection.Week:
		return "New week: roll over"
	case projection.Sprint:
		return "New sprint: roll over"
	}
	return ""
}

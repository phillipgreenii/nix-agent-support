package config

import "fmt"

// Thread-activity settings: the rule is dormant by default (plan mode, so it
// proposes annotations and apply writes none) and accepts records of the
// listed sources only. Both are data, never code paths per source.
const (
	// ThreadActivityModePlan is the default: the rule's actions are listed by
	// plan and held back by apply.
	ThreadActivityModePlan = "plan"
	// ThreadActivityModeApply lets apply write the rule's annotations. Flipping
	// to it is the operator's call.
	ThreadActivityModeApply = "apply"
)

// DefaultThreadActivitySources is thread_activity_sources when the file sets
// none: the chat source only.
var DefaultThreadActivitySources = []string{"slack"}

// ThreadActivityAppliesWrites reports whether apply writes the
// thread-activity rule's annotations (thread_activity_mode is "apply"). Unset
// and "plan" are plan mode.
func (c *Config) ThreadActivityAppliesWrites() bool {
	return c != nil && c.ThreadActivityMode == ThreadActivityModeApply
}

// ThreadActivitySourceList is the accepted source list: the configured
// thread_activity_sources, or DefaultThreadActivitySources when none is set.
func (c *Config) ThreadActivitySourceList() []string {
	if c == nil || len(c.ThreadActivitySources) == 0 {
		return DefaultThreadActivitySources
	}
	return c.ThreadActivitySources
}

func validateThreadActivityKeys(c *Config) error {
	switch c.ThreadActivityMode {
	case "", ThreadActivityModePlan, ThreadActivityModeApply:
	default:
		return fmt.Errorf("thread_activity_mode is %q; it must be %q or %q", c.ThreadActivityMode, ThreadActivityModePlan, ThreadActivityModeApply)
	}
	for _, s := range c.ThreadActivitySources {
		if s == "" {
			return fmt.Errorf("thread_activity_sources holds a blank source")
		}
	}
	return nil
}

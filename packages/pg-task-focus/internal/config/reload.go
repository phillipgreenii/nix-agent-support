package config

import "fmt"

// CheckReload decides whether next may replace prev while the profile named
// activeProfile is active (INV-CONF-12): a reload that no longer defines the
// active profile is a bad reload, and the caller keeps the previous
// configuration and reports the problem. Removing any other profile, a task
// or a cycle type is allowed, since history does not consult the
// configuration. An empty activeProfile means no profile has been activated
// yet, and nothing is checked. prev may be nil on a first load. The error is a
// *ValidationError, the type that reports a bad file, so a daemon reports both
// the same way.
//
// When prev is nil, or never defined the active profile, nothing is removed and
// the problem says the active profile is not defined in the new configuration.
//
// A document that fails Parse has no *Config and so never reaches this check.
func CheckReload(prev, next *Config, activeProfile string) error {
	if next == nil {
		return &ValidationError{Problems: []Problem{{Message: "the new configuration is missing: nothing to reload"}}}
	}
	if activeProfile == "" {
		return nil
	}
	if _, ok := next.profiles[activeProfile]; ok {
		return nil
	}
	// A reload removes the profile only when the previous configuration had it.
	if _, had := prev.profile(activeProfile); had {
		return &ValidationError{Problems: []Problem{{
			Path: "/profiles/" + escapePointer(activeProfile),
			Message: fmt.Sprintf("the reload removes profile %q, which is the active profile; "+
				"switch to another profile first, or keep %q in the configuration", activeProfile, activeProfile),
		}}}
	}
	return &ValidationError{Problems: []Problem{{
		Path: "/profiles/" + escapePointer(activeProfile),
		Message: fmt.Sprintf("the active profile %q is not defined in the new configuration; "+
			"define it there, or switch to another profile first", activeProfile),
	}}}
}

// RestartRequired lists the settings that differ between prev and next and
// take effect only on restart (INV-CONF-13): "listen_port" and "public_url",
// in that order. A reload that sees one MUST report it. Nothing else is
// listed: every other setting is read at run time. A nil prev (a first load)
// reports nothing.
func RestartRequired(prev, next *Config) []string {
	if prev == nil || next == nil {
		return nil
	}
	var out []string
	if prev.listenPort != next.listenPort {
		out = append(out, "listen_port")
	}
	if prev.publicURL != next.publicURL {
		out = append(out, "public_url")
	}
	return out
}

// profile reports the profile called name; a nil Config defines none.
func (c *Config) profile(name string) (Profile, bool) {
	if c == nil {
		return Profile{}, false
	}
	return c.Profile(name)
}

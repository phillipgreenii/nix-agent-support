//go:build linux

package proc

// SupervisorProbe reports SupervisorNone: Linux has no supervisor integration
// until pg2-opk5g (the systemd equivalent of the launchd registration helper).
func SupervisorProbe(label string) SupervisorState { return SupervisorNone }

// RestartHint is the text an unavailable answer carries so a caller can bring
// the daemon back. The command MUST run in the background (nohup ... &): an
// agent that ran a bare foreground serve from an unavailable answer would
// block its shell.
func RestartHint(label, configPath string) string {
	return "no supervisor on this host (pg2-opk5g); start it in the background: " +
		"nohup pg-connector-github --config " + shellQuote(configPath) + " serve >/dev/null 2>&1 &"
}

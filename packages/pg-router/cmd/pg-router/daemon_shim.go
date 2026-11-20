package main

import "github.com/phillipgreenii/pg-router/internal/daemon"

// The tick loop and core boot live in internal/daemon. These package-level
// bindings keep this package's call sites (and its tests) on their familiar
// names while the daemon package owns the implementations.
var (
	bootCore            = daemon.BootCore
	runOneTick          = daemon.RunOneTick
	handlerCommandFor   = daemon.HandlerCommandFor
	pullSourceIntervals = daemon.PullSourceIntervals
	newActivityObserver = daemon.NewActivityObserver
	sourceReportsFor    = daemon.SourceReportsFor
	attachCalendar      = daemon.AttachCalendar
	resolvedConfigFor   = daemon.ResolvedConfigFor
	pauseNotice         = daemon.PauseNotice
	routerPause         = daemon.RouterPause
	bindingInfo         = daemon.BindingInfo
	roleSurvives        = daemon.RoleSurvives
	survivingRoles      = daemon.SurvivingRoles
	loadBackoff         = daemon.LoadBackoff
)

// version is the link-time build version (-X main.version); the daemon package
// reports it in its metrics build info and status snapshots.
func init() { daemon.Version = version }

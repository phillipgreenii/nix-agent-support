//go:build !darwin

package eventkitprovider

import (
	"github.com/phillipgreenii/osx-bridge-api/internal/calendarapi"
	"github.com/phillipgreenii/osx-bridge-api/internal/wire"
)

// Provider is the non-darwin stand-in: EventKit exists only on macOS, so
// every method reports wire.ErrUnavailable. This file exists solely so the
// module builds and `go test ./...`/`nix flake check` succeed on every
// system this flake evaluates for (aarch64-linux, x86_64-linux, plus the
// two darwin systems) — mirroring go-eventkit's own
// bridge_darwin.go/bridge_other.go split. osx-bridge-api is never deployed
// on a non-darwin system (its only consumer is a
// phillipgreenii.system.launchdServices.userAgents LaunchAgent, a
// darwin-only concept).
type Provider struct{}

// New always fails on non-darwin platforms.
func New() (*Provider, error) {
	return nil, wire.WrapError(wire.ErrUnavailable, "osx-bridge-api: EventKit is only available on macOS (darwin)")
}

// Calendars implements calendarapi.Provider.
func (p *Provider) Calendars() ([]calendarapi.Calendar, error) {
	return nil, wire.WrapError(wire.ErrUnavailable, "osx-bridge-api: EventKit is only available on macOS (darwin)")
}

// Events implements calendarapi.Provider.
func (p *Provider) Events(calendarapi.EventsQuery) ([]calendarapi.Event, error) {
	return nil, wire.WrapError(wire.ErrUnavailable, "osx-bridge-api: EventKit is only available on macOS (darwin)")
}

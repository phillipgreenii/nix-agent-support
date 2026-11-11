// Package vpn implements vpn-sensor: HTTPS HEAD of the configured hosts of a
// private network, answering the one resource named by the config key
// resource (network_<private> in a deployment), with the same reachability
// rule as host-sensor's network resource (see netprobe).
//
// Config keys: resource (required, must start with "network_"), hosts
// (required list of bare hostnames, probed as https://<host>/), timeout
// (duration, default 5s), failures (integer >= 1, default 3). The unknown
// value of the resource is best.
//
// Exported names: Sensor, New.
package vpn

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/netprobe"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

// Sensor is the vpn-sensor probe set.
type Sensor struct {
	Prober    *netprobe.Prober
	Resource  string
	Hosts     []string
	Threshold int
}

// New returns a Sensor on the production HTTP client. It answers nothing until
// configured with a resource and hosts.
func New() *Sensor {
	return &Sensor{Prober: &netprobe.Prober{}, Threshold: netprobe.DefaultThreshold}
}

// Resources implements sensorcli.Sensor.
func (s *Sensor) Resources() []string {
	if s.Resource == "" {
		return nil
	}
	return []string{s.Resource}
}

// Bands implements sensorcli.Sensor: a two-level scale has no bands.
func (s *Sensor) Bands() []levels.Info {
	if s.Resource == "" {
		return nil
	}
	return []levels.Info{levels.NoBands(s.Resource, "targets_reachable")}
}

// Configure implements sensorcli.Sensor.
func (s *Sensor) Configure(cfg sensorcli.Config) error {
	if err := cfg.CheckKeys("resource", "hosts", "timeout", "failures"); err != nil {
		return err
	}
	res, err := cfg.String("resource", "")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(res, "network_") || len(res) == len("network_") {
		return fmt.Errorf("config key resource: want network_<private>, got %q", res)
	}
	hosts, err := cfg.StringList("hosts")
	if err != nil {
		return err
	}
	if len(hosts) == 0 {
		return fmt.Errorf("config key hosts: want a non-empty list of hostnames")
	}
	for _, h := range hosts {
		if !netprobe.ValidHost(h) {
			return fmt.Errorf("config key hosts: %q is not a bare hostname", h)
		}
	}
	if s.Threshold == 0 {
		s.Threshold = netprobe.DefaultThreshold
	}
	if err := netprobe.ConfigureCommon(cfg, "timeout", "failures", s.Prober, &s.Threshold); err != nil {
		return err
	}
	s.Resource, s.Hosts = res, hosts
	return nil
}

// Measure implements sensorcli.Sensor.
func (s *Sensor) Measure(ctx context.Context, env sensorcli.Env) ([]levels.Observation, error) {
	if s.Resource == "" || len(s.Hosts) == 0 {
		return nil, levels.ErrNoReading
	}
	if env.Resource != "" && env.Resource != s.Resource {
		return nil, levels.ErrNoReading
	}
	return []levels.Observation{netprobe.Observe(ctx, s.Prober, env, s.Resource, netprobe.HostURLs(s.Hosts), s.Threshold)}, nil
}

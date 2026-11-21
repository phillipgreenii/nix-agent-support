// Package vpn implements vpn-sensor: HTTPS HEAD of the configured hosts of a
// private network, answering ONE resource per invocation: the reachability
// resource (config key resource, network_<private> in a deployment). A target
// counts reachable when the TCP connect, the TLS handshake and an HTTP
// response complete, EVEN IF the certificate does not verify (a private CA
// this machine does not trust must not read as the VPN being down;
// reachability is not trust).
//
// The resource has the same rule as host-sensor's network resource (see
// netprobe): UNAVAILABLE only when all targets fail on 3 consecutive passes.
//
// Config keys: resource (required, must start with "network_"), hosts
// (required list of bare hostnames, probed as https://<host>/), timeout
// (duration, default 5s), failures (integer >= 1, default 3). The retired
// trust_resource key is still accepted and ignored so an existing config
// keeps loading. The unknown value of the resource is best.
//
// Exported names: Sensor, New.
package vpn

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-router/internal/sensors/levels"
	"github.com/phillipgreenii/pg-router/internal/sensors/netprobe"
	"github.com/phillipgreenii/pg-router/internal/sensors/sensorcli"
)

// Sensor is the vpn-sensor probe set.
type Sensor struct {
	// Reach does not verify certificates (the reachability resource).
	Reach     *netprobe.Prober
	Resource  string
	Hosts     []string
	Threshold int
}

// New returns a Sensor on the production HTTP client. It answers nothing until
// configured with a resource and hosts.
func New() *Sensor {
	return &Sensor{Reach: &netprobe.Prober{SkipVerify: true}, Threshold: netprobe.DefaultThreshold}
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
	if err := cfg.CheckKeys("resource", "trust_resource", "hosts", "timeout", "failures"); err != nil {
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
	if s.Reach == nil {
		s.Reach = &netprobe.Prober{}
	}
	s.Reach.SkipVerify = true
	if err := netprobe.ConfigureCommon(cfg, "timeout", "failures", s.Reach, &s.Threshold); err != nil {
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
	reach := s.Reach.Probe(ctx, netprobe.HostURLs(s.Hosts))
	return []levels.Observation{netprobe.ObserveResults(env, s.Resource, reach, s.Threshold)}, nil
}

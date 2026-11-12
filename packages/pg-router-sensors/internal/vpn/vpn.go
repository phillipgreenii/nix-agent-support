// Package vpn implements vpn-sensor: HTTPS HEAD of the configured hosts of a
// private network, answering TWO resources per invocation:
//
//   - the reachability resource (config key resource, network_<private> in a
//     deployment): a target counts reachable when the TCP connect, the TLS
//     handshake and an HTTP response complete, EVEN IF the certificate does
//     not verify (a private CA this machine does not trust must not read as
//     the VPN being down; reachability is not trust);
//   - the trust resource (config key trust_resource, default
//     network_zr_trust): the same HEAD WITH normal verification against the
//     system roots.
//
// Each resource has its own all-fail counter and dwell state and the same
// rule as host-sensor's network resource (see netprobe): UNAVAILABLE only
// when all targets fail on 3 consecutive passes.
//
// Config keys: resource (required, must start with "network_"), trust_resource
// (must start with "network_" and differ from resource, default
// network_zr_trust), hosts (required list of bare hostnames, probed as
// https://<host>/), timeout (duration, default 5s), failures (integer >= 1,
// default 3). The unknown value of each resource is best.
//
// Exported names: Sensor, New, DefaultTrustResource.
package vpn

import (
	"context"
	"fmt"
	"strings"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/netprobe"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

// DefaultTrustResource is the trust resource name when trust_resource is not
// configured.
const DefaultTrustResource = "network_zr_trust"

// Sensor is the vpn-sensor probe set.
type Sensor struct {
	// Prober verifies certificates (the trust resource).
	Prober *netprobe.Prober
	// Reach does not verify certificates (the reachability resource).
	Reach         *netprobe.Prober
	Resource      string
	TrustResource string
	Hosts         []string
	Threshold     int
}

// New returns a Sensor on the production HTTP client. It answers nothing until
// configured with a resource and hosts.
func New() *Sensor {
	return &Sensor{Prober: &netprobe.Prober{}, Reach: &netprobe.Prober{SkipVerify: true}, Threshold: netprobe.DefaultThreshold}
}

// Resources implements sensorcli.Sensor.
func (s *Sensor) Resources() []string {
	if s.Resource == "" {
		return nil
	}
	return []string{s.Resource, s.TrustResource}
}

// Bands implements sensorcli.Sensor: a two-level scale has no bands.
func (s *Sensor) Bands() []levels.Info {
	if s.Resource == "" {
		return nil
	}
	return []levels.Info{levels.NoBands(s.Resource, "targets_reachable"), levels.NoBands(s.TrustResource, "targets_reachable")}
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
	trust, err := cfg.String("trust_resource", DefaultTrustResource)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(trust, "network_") || len(trust) == len("network_") {
		return fmt.Errorf("config key trust_resource: want network_<private>, got %q", trust)
	}
	if trust == res {
		return fmt.Errorf("config key trust_resource: must differ from resource %q", res)
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
	if s.Reach == nil {
		s.Reach = &netprobe.Prober{}
	}
	s.Reach.SkipVerify = true
	s.Reach.Timeout = s.Prober.Timeout
	s.Resource, s.TrustResource, s.Hosts = res, trust, hosts
	return nil
}

// Measure implements sensorcli.Sensor.
func (s *Sensor) Measure(ctx context.Context, env sensorcli.Env) ([]levels.Observation, error) {
	if s.Resource == "" || len(s.Hosts) == 0 {
		return nil, levels.ErrNoReading
	}
	if env.Resource != "" && env.Resource != s.Resource && env.Resource != s.TrustResource {
		return nil, levels.ErrNoReading
	}
	urls := netprobe.HostURLs(s.Hosts)
	var out []levels.Observation
	if env.Resource == "" || env.Resource == s.Resource {
		out = append(out, netprobe.Observe(ctx, s.Reach, env, s.Resource, urls, s.Threshold))
	}
	if env.Resource == "" || env.Resource == s.TrustResource {
		out = append(out, netprobe.Observe(ctx, s.Prober, env, s.TrustResource, urls, s.Threshold))
	}
	return out, nil
}

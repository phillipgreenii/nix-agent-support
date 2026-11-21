package telemetry

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/daemon/otelsetup"
)

// ServiceName is the service.name resource attribute of every export.
const ServiceName = "pg-connector-issue-jira"

// DefaultInterval is how often the periodic reader exports when Config.Interval
// is zero.
const DefaultInterval = otelsetup.DefaultInterval

// Config is what Setup needs from the daemon's typed config.
type Config struct {
	// Endpoint is the OTLP/gRPC endpoint URL (config.Config.OTelEndpoint);
	// empty or blank means no export.
	Endpoint string
	// ServiceVersion is the build's version, stamped as service.version when
	// non-empty.
	ServiceVersion string
	// Interval is the metric export (and gauge poll) interval; zero means
	// DefaultInterval.
	Interval time.Duration
}

// Setup installs the global meter and tracer providers through the kernel's
// base telemetry (pkg/connectord/daemon/otelsetup) under this daemon's
// service name.
func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	return otelsetup.Setup(ctx, otelsetup.Config{
		ServiceName:    ServiceName,
		Endpoint:       cfg.Endpoint,
		ServiceVersion: cfg.ServiceVersion,
		Interval:       cfg.Interval,
	})
}

// Package telemetry is pg-connector-github's OpenTelemetry layer: the meter and
// tracer providers (Setup), the instruments of the daemon's metric table and
// the request and origin-call spans (Telemetry).
//
// # Where the OTel dependency lives
//
// The umbrella pg-connector and the shared library pkg/connectord stay unbound
// to OpenTelemetry (see the pkg/eventlog doc comment); this package, inside the
// binary's own internal/ tree, is the only place that imports the SDK. The
// library exposes plain observers and state snapshots (governor OnSpend and
// State, scheduler State, router Use, the summary service's OnBatchSent) and
// this package subscribes to them.
//
// # Export
//
// Setup exports over OTLP/gRPC to the endpoint named in the daemon's rendered
// config, the same transport as the workspace's other daemons (pa-monitor,
// ccpool): an "http://host:port" endpoint is insecure gRPC and "https://" is
// TLS. With no endpoint, Setup installs no-op providers and returns a no-op
// shutdown: nothing is constructed, nothing dials, and the daemon runs
// normally.
//
// # Names and labels
//
// Metric names are dotted after the pa-monitor convention, prefixed
// "pg_connector_github.". A label value is always drawn from a closed
// vocabulary the code defines (a bucket, a class, a role, a registered op, a
// handler's served_from word, a reduced pause reason, a change kind from
// classify.Kinds(), one of the four origin words, a settle result word, a
// configured query name, a consumer name that is stored in the consumer table);
// an entity id, a request argument or an ad-hoc query key is never a label. A
// value the code cannot bound is folded to a fixed word ("unknown", "none",
// "other"): "unknown" for a change kind, origin or result word, "other" for a
// consumer name that is not stored and a query name that is not configured.
package telemetry

import (
	"context"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/daemon/otelsetup"
)

// ServiceName is the service.name resource attribute of every export.
const ServiceName = "pg-connector-github"

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

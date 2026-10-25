// Package telemetry is pg-connector-issue-jira's OpenTelemetry layer: the meter
// and tracer providers (Setup), the instruments of the daemon's metric table and
// the request and origin-call spans (Telemetry).
//
// # Where the OTel dependency lives
//
// The umbrella pg-connector and the shared library pkg/connectord stay unbound
// to OpenTelemetry; this package, inside the binary's own internal/ tree, is the
// only place in the Jira daemon that imports the SDK. The library exposes plain
// observers and state snapshots (governor OnSpend and State, scheduler State,
// router Use, the change engine's and the change feed's observers) and this
// package subscribes to them. The change instruments themselves live in
// pkg/connectord/changemetrics and are registered here under the Jira prefix.
//
// # Export
//
// Setup exports over OTLP/gRPC to the endpoint named in the daemon's rendered
// config: an "http://host:port" endpoint is insecure gRPC and "https://" is TLS.
// With no endpoint, Setup installs no-op providers and returns a no-op shutdown:
// nothing is constructed, nothing dials, and the daemon runs normally.
//
// # Names and labels
//
// Metric names are dotted, prefixed "pg_connector_issue_jira." and registered
// without a _total suffix. A label value is always drawn from a closed
// vocabulary the code defines (a request result word, a registered op, a
// handler's served_from word, a reduced pause reason, a priority class, a field
// group, a change kind of the daemon's change registration, one of the four
// origin words, a settle result word, a configured query name, a consumer name
// that is stored in the consumer table, one of the three tracked-entity
// classes); an issue key, a request argument or an ad-hoc query key is never a
// label. A value the code cannot bound is folded to a fixed word: "unknown" for
// an op, group, change kind, origin or result word, "other" for a consumer name
// that is not stored and a query name that is not configured, and "error" for a
// request result that is not one of the five words.
//
// The committed catalog of every series is
// packages/pg-connector/grafana/pg-connector-issue-jira-metrics.txt; a test of
// this package fails when it and the Metric* constants disagree.
package telemetry

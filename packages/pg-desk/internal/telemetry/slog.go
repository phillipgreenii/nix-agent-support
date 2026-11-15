// Package telemetry — slog<->OTel glue.
//
// NewSlogHandler builds the otelslog bridge bound to the global
// LoggerProvider Init installs; serve fans its log-file handler and this
// bridge out through x/jsonllogger.Fanout, so every record also reaches OTLP.
// See telemetry.go's package doc for why this package otherwise duplicates
// the retired PR tool's proven pattern rather than importing it across the
// module boundary.
package telemetry

import (
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
)

// NewSlogHandler returns an slog.Handler that exports records to the global
// OTel LoggerProvider (installed by Init). When no OTLP endpoint is
// configured the global provider is a no-op, so this handler is a cheap
// no-op and is safe to include unconditionally. The instrumentation scope
// name is scopeName; it does NOT set service_name — that comes from the
// resource (OTEL_SERVICE_NAME) configured in Init.
func NewSlogHandler() slog.Handler {
	return otelslog.NewHandler(scopeName)
}

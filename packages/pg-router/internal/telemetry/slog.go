// Package telemetry — slog<->OTel glue.
//
// NewSlogHandler builds the otelslog bridge that exports every record over OTLP
// (combined with stderr logging through x/jsonllogger.Fanout), bound to the
// global LoggerProvider Init installs.
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

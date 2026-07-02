package otel

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
)

func buildResource(ctx context.Context, serviceName, serviceVersion string) (*resource.Resource, error) {
	return resource.New(
		ctx,
		// WithFromEnv first so the explicit attributes below win on key
		// conflict (later options take precedence in the merge).
		resource.WithFromEnv(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", serviceVersion),
		),
	)
}

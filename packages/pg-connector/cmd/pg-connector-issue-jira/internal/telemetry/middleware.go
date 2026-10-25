package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Middleware is the router's request middleware (router.Router.Use): it opens
// the request span, runs the request, then records request.duration and
// completes the span with what the handler annotated (served_from, the wait).
// The span's context reaches the handler, so RecordSpend called with it
// parents each origin call under the request.
func (t *Telemetry) Middleware() router.Middleware {
	return func(next router.Func) router.Func {
		return func(ctx context.Context, c router.Call) scriptout.Response {
			op := t.opLabel(c)
			start := t.now()
			ctx, span := t.tracer.Start(ctx, spanRequest,
				trace.WithTimestamp(start),
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(attribute.String(keyOp, op)))

			resp := next(ctx, c)

			end := t.now()
			ann := router.AnnotationsFrom(ctx)
			served := servedFromLabel(ann.ServedFrom)
			span.SetAttributes(
				attribute.String(keyServedFrom, served),
				attribute.Float64("wait_seconds", ann.Wait.Seconds()),
			)
			if resp.Error != nil {
				span.SetStatus(codes.Error, resp.Error.Code)
				span.SetAttributes(attribute.String("error.code", resp.Error.Code))
			}
			span.End(trace.WithTimestamp(end))

			t.duration.Record(ctx, end.Sub(start).Seconds(), metric.WithAttributes(
				attribute.String(keyOp, op),
				attribute.String(keyServedFrom, served),
			))
			return resp
		}
	}
}

// opLabel is the op's name when it is registered and "unknown" otherwise, so a
// client cannot mint label values by sending made-up ops.
func (t *Telemetry) opLabel(c router.Call) string {
	op := c.Request.Op
	if op == "" || (t.opts.KnownOp != nil && !t.opts.KnownOp(c.Instance, op)) {
		return unknown
	}
	return op
}

func servedFromLabel(s string) string {
	if s == "" {
		return none
	}
	return s
}

// tracing.go — span helpers for the sync engine.
//
// All sync engine spans flow through these helpers so the attribute set
// stays bounded to what the design spec allows: repo_name, pr_number,
// run_id, author_email. Do NOT add comment body or other free-form
// content here — that data is intentionally kept off traces (see
// docs/superpowers/specs/2026-05-19-pg-pr-design.md §"OTEL + Prometheus
// instrumentation").

package sync

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
)

// startRepoSpan opens the top-level per-repo sync span. The returned
// ctx carries the span; callers MUST defer span.End().
func startRepoSpan(ctx context.Context, repo string) (context.Context, trace.Span) {
	return telemetry.Tracer().Start(
		ctx, "pg-pr.sync.repo",
		trace.WithAttributes(attribute.String("repo_name", repo)),
	)
}

// startPRSpan opens a per-PR span nested under a repo span. The author
// argument is treated as an email when non-empty; pass "" to skip.
func startPRSpan(ctx context.Context, repo string, prNumber int, author string) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String("repo_name", repo),
		attribute.Int("pr_number", prNumber),
	}
	if author != "" {
		attrs = append(attrs, attribute.String("author_email", author))
	}
	return telemetry.Tracer().Start(
		ctx, "pg-pr.sync.pr",
		trace.WithAttributes(attrs...),
	)
}

// startVCSSpan opens a span for a VCS provider call. method is the
// Provider method name (e.g., "ListMyPRs"); prNumber may be 0 when the
// call is repo-scoped rather than PR-scoped.
func startVCSSpan(ctx context.Context, method, repo string, prNumber int) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{attribute.String("repo_name", repo)}
	if prNumber > 0 {
		attrs = append(attrs, attribute.Int("pr_number", prNumber))
	}
	return telemetry.Tracer().Start(
		ctx, "pg-pr.provider.vcs."+method,
		trace.WithAttributes(attrs...),
	)
}

// startCICDSpan opens a span for a CICD provider call.
func startCICDSpan(ctx context.Context, method, repo string, prNumber int, runID string) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{attribute.String("repo_name", repo)}
	if prNumber > 0 {
		attrs = append(attrs, attribute.Int("pr_number", prNumber))
	}
	if runID != "" {
		attrs = append(attrs, attribute.String("run_id", runID))
	}
	return telemetry.Tracer().Start(
		ctx, "pg-pr.provider.cicd."+method,
		trace.WithAttributes(attrs...),
	)
}

// recordSpanErr stamps an error onto the span. No-op when err is nil.
func recordSpanErr(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

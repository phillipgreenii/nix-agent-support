package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/command"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/jsonstrict"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

// maxBody bounds a request body: far above the 256 KiB an event may be, far
// below anything that could hurt a single-user daemon.
const maxBody = 1 << 20

const problemJSON = "application/problem+json"

// writeJSON sends v as the response body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encoding the response failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// writeProblem sends an RFC 9457 problem and records its reason on the request.
func (s *Server) writeProblem(w http.ResponseWriter, r *http.Request, p wire.Problem) {
	ri := info(r.Context())
	ri.reason = p.Reason
	if p.TraceID == "" {
		p.TraceID = ri.traceID
	}
	b, err := json.Marshal(p)
	if err != nil {
		http.Error(w, "encoding the problem failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", problemJSON)
	w.WriteHeader(p.Status)
	_, _ = w.Write(append(b, '\n'))
}

// invalid sends a 400 invalid_request with a sentence.
func (s *Server) invalid(w http.ResponseWriter, r *http.Request, format string, args ...any) {
	s.writeProblem(w, r, wire.Simple(command.ReasonInvalidRequest, fmt.Sprintf(format, args...), r.URL.Path, info(r.Context()).traceID))
}

// notReady sends 503 not_ready: replay has not finished or the first
// writability check has not passed.
func (s *Server) notReady(w http.ResponseWriter, r *http.Request) {
	check, _ := s.failing.Load().(string)
	detail := "The service is starting: replay has not finished or the first store check has not passed."
	if check != "" {
		detail += " Failing check: " + check + "."
	}
	s.writeProblem(w, r, wire.Simple(command.ReasonNotReady, detail, r.URL.Path, info(r.Context()).traceID))
}

// ready returns the engine once the server is ready, or answers 503
// not_ready and returns false.
func (s *Server) requireEngine(w http.ResponseWriter, r *http.Request) (*engine.Engine, bool) {
	e := s.engine()
	if e == nil || !s.ready.Load() {
		s.notReady(w, r)
		return nil, false
	}
	return e, true
}

// writeError answers an error from the engine: a refusal becomes its problem;
// anything else is a defect, or a request whose own context ended.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, e *engine.Engine, err error) {
	if rej, ok := wire.AsRejection(err); ok {
		var st *wire.Store
		if rej.Reason == command.ReasonStoreUnavailable {
			v := wire.FromStoreHealth(s.storeHealth(e))
			st = &v
		}
		p := wire.ProblemOf(rej, r.URL.Path, info(r.Context()).traceID, st)
		s.writeProblem(w, r, p)
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		s.writeProblem(w, r, wire.Simple(command.ReasonNotReady, "The request was cancelled before it was handled; nothing was recorded.", r.URL.Path, info(r.Context()).traceID))
		return
	}
	s.log.ErrorContext(r.Context(), "request failed with a defect", "request_id", info(r.Context()).id, "error", err.Error())
	s.writeProblem(w, r, wire.Simple(wire.ReasonInternal, "The request failed because of a defect in the daemon; report the trace_id.", r.URL.Path, info(r.Context()).traceID))
}

// readJSON reads and checks a request body and decodes it into v. A body that
// is not valid UTF-8 is refused before decoding can replace the bad bytes with
// a placeholder (INV-LOG-27); a duplicate key, an unknown field and trailing
// data are refused too. It reports false after answering.
func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.invalid(w, r, "The request body is longer than %d bytes.", maxBody)
		} else {
			s.invalid(w, r, "The request body could not be read: %v.", err)
		}
		return false
	}
	if !utf8.Valid(body) {
		s.invalid(w, r, "The request body is not valid UTF-8, which is refused rather than rewritten.")
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		s.invalid(w, r, "The request body is empty; send a JSON object, even {}.")
		return false
	}
	if err := jsonstrict.CheckNoDuplicateKeys(body); err != nil {
		s.invalid(w, r, "The request body is not acceptable JSON: %v.", err)
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		s.invalid(w, r, "The request body does not match the endpoint's schema: %s.", sentence(err))
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		s.invalid(w, r, "The request body has data after the JSON object.")
		return false
	}
	return true
}

func sentence(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// respondMutation runs the command through the engine and answers: the result
// of a mutation, or the preview of a dry run. A mutation also writes its one
// Info line, with the event id and event type, and gets child spans for the
// stages the engine reports.
func (s *Server) respondMutation(w http.ResponseWriter, r *http.Request, c command.Command) {
	e, ok := s.requireEngine(w, r)
	if !ok {
		return
	}
	res, err := e.Do(r.Context(), c)
	if err != nil {
		s.writeError(w, r, e, err)
		return
	}
	s.stageSpans(r.Context(), res.Stages)
	if c.IsDryRun() {
		writeJSON(w, http.StatusOK, wire.FromDryRun(res))
		return
	}
	out := wire.FromResult(res)
	ri := info(r.Context())
	ri.addFields(slog.Bool("changed", res.Changed), slog.Int("events", len(res.EventIDs)))
	if res.Replayed {
		ri.addFields(slog.Bool("replayed", true))
	}
	if len(res.EventIDs) > 0 {
		id := res.EventIDs[0]
		typ := c.Name()
		if v, found := e.Snapshot().Model.Event(id); found {
			typ = string(v.Original.Payload.EventType())
		}
		ri.addFields(slog.String("event_id", string(id)), slog.String("event_type", typ))
		if span := trace.SpanFromContext(r.Context()); span.IsRecording() {
			obs.Attrs(span, attribute.String("event_id", string(id)), attribute.String("event_type", typ), attribute.Bool("changed", res.Changed))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// stageSpans records the stages of a handled request as child spans of the
// request, with the instants the engine measured: validate, append, the fsync
// inside it, and project.
func (s *Server) stageSpans(ctx context.Context, stages []engine.Stage) {
	var appendCtx context.Context
	for _, st := range stages {
		parent := ctx
		if st.Name == "fsync" && appendCtx != nil {
			parent = appendCtx
		}
		c, span := s.tracer.Start(parent, "pg-task-focus."+st.Name, trace.WithTimestamp(st.Start))
		if st.Name == "append" {
			appendCtx = c
		}
		obs.Attrs(span, attribute.String("stage", st.Name))
		span.End(trace.WithTimestamp(st.End))
	}
}

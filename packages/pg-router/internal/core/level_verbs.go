package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/schemas"
)

// The socket verbs of the level command family: level-ingest (the push path of
// a sensor), level-list, level-set, level-clear and level-refresh. The CLI verbs
// level list|set|clear|refresh, pause and resume are clients of these.
//
// Exported names: SubcommandLevelIngest, SubcommandLevelList, SubcommandLevelSet,
// SubcommandLevelClear, SubcommandLevelRefresh, the Level*Schema constants,
// MaxOverride, RouterResource, LevelPaused, LevelRefresher, LevelRefreshResult,
// Options.Levels and Options.LevelRefresher (set on Service by Listen),
// LevelEntries (the level-list / status entry builder).

// The socket verbs.
const (
	SubcommandLevelIngest  = "level-ingest"
	SubcommandLevelList    = "level-list"
	SubcommandLevelSet     = "level-set"
	SubcommandLevelClear   = "level-clear"
	SubcommandLevelRefresh = "level-refresh"
)

// The message types backing the verbs (schemas/).
const (
	LevelIngestRequestSchema  = "cli.level-ingest"
	LevelIngestReplySchema    = "cli.level-ingest-reply"
	LevelListRequestSchema    = "cli.level-list"
	LevelListReplySchema      = "cli.level-list-reply"
	LevelSetRequestSchema     = "cli.level-set"
	LevelSetReplySchema       = "cli.level-set-reply"
	LevelClearRequestSchema   = "cli.level-clear"
	LevelClearReplySchema     = "cli.level-clear-reply"
	LevelRefreshRequestSchema = "cli.level-refresh"
	LevelRefreshReplySchema   = "cli.level-refresh-reply"
)

// MaxOverride is the longest override that is not until-cleared: a longer one is
// rejected, never clamped.
const MaxOverride = 24 * time.Hour

// RouterResource and LevelPaused name the router's own resource and its paused
// level: pause and resume are level-set and level-clear on them.
const (
	RouterResource = eventqueue.RouterResource
	LevelPaused    = eventqueue.LevelPaused
)

// LevelRefreshResult is the outcome of refreshing one resource: outcome is
// applied, failed or no_source.
type LevelRefreshResult struct {
	Resource string
	Outcome  string
	Level    string
	Reason   string
}

// LevelRefresher runs a sensor's pull query once, now, for each named resource
// (every level source when resources is empty) and applies the answers.
type LevelRefresher interface {
	RefreshLevels(ctx context.Context, resources []string) []LevelRefreshResult
}

func (s *Service) noLevels(stdout io.Writer, verb string) bool {
	if s.levels == nil {
		writeBody(stdout, errorReply(verb+": levels are not available on this core"))
		return true
	}
	return false
}

// handleLevelIngest runs the push path: the request IS a level answer, validated
// and applied by the projection (malformed answers are rejected and counted there).
func (s *Service) handleLevelIngest(stdin io.Reader, stdout io.Writer) int {
	const verb = SubcommandLevelIngest
	if s.noLevels(stdout, verb) {
		return conformance.ExitError
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		writeBody(stdout, errorReply(verb+": read request: "+err.Error()))
		return conformance.ExitError
	}
	outcome, reason, aerr := s.levels.IngestJSON(data)
	if aerr != nil {
		writeBody(stdout, errorReply(verb+": level accepted but the durable append failed: "+aerr.Error()))
		return conformance.ExitError
	}
	reply := map[string]any{"schemaVersion": schemas.SchemaVersion, "outcome": string(outcome)}
	if outcome == LevelRejected {
		reply["reason"] = reason
	}
	return writeJSONReply(stdout, verb, reply)
}

// LevelEntries renders the level-list / status entries of the projection.
func LevelEntries(p *LevelProjection) []map[string]any {
	details := p.Details()
	out := make([]map[string]any, 0, len(details))
	for _, d := range details {
		e := map[string]any{"resource": d.Resource}
		if d.HasLevel {
			e["level"] = d.Level
			e["source"] = d.Source
			e["owner"] = d.Owner
			e["observed_at"] = d.ObservedAt.UTC().Format(time.RFC3339Nano)
			e["received_at"] = d.ReceivedAt.UTC().Format(time.RFC3339Nano)
			if !d.ExpiresAt.IsZero() {
				e["expires_at"] = d.ExpiresAt.UTC().Format(time.RFC3339Nano)
			}
			e["age_s"] = int64(d.Age / time.Second)
			e["stale"] = d.Stale
			e["since"] = d.Since.UTC().Format(time.RFC3339Nano)
			if d.Reading != nil {
				e["reading"] = map[string]any{"value": d.Reading.Value, "unit": d.Reading.Unit}
			}
			if d.Override != nil {
				ov := map[string]any{"until_cleared": d.Override.UntilCleared}
				if !d.Override.UntilCleared {
					ov["expires_at"] = d.Override.ExpiresAt.UTC().Format(time.RFC3339Nano)
				}
				e["override"] = ov
			}
		}
		out = append(out, e)
	}
	return out
}

func (s *Service) handleLevelList(stdin io.Reader, stdout io.Writer) int {
	const verb = SubcommandLevelList
	if s.noLevels(stdout, verb) {
		return conformance.ExitError
	}
	var req struct{}
	if !decodeVerb(stdin, stdout, verb, LevelListRequestSchema, &req) {
		return conformance.ExitError
	}
	return writeJSONReply(stdout, verb, map[string]any{"schemaVersion": schemas.SchemaVersion, "levels": LevelEntries(s.levels)})
}

func (s *Service) handleLevelSet(stdin io.Reader, stdout io.Writer) int {
	const verb = SubcommandLevelSet
	if s.noLevels(stdout, verb) {
		return conformance.ExitError
	}
	var req struct {
		Resource     string `json:"resource"`
		Level        string `json:"level"`
		Owner        string `json:"owner"`
		ForS         *int64 `json:"for_s"`
		UntilCleared bool   `json:"until_cleared"`
	}
	if !decodeVerb(stdin, stdout, verb, LevelSetRequestSchema, &req) {
		return conformance.ExitError
	}
	if (req.ForS == nil) == !req.UntilCleared {
		writeBody(stdout, errorReply(verb+": exactly one of for_s and until_cleared is required"))
		return conformance.ExitError
	}
	if err := s.levels.ValidateLevel(req.Resource, req.Level); err != nil {
		writeBody(stdout, errorReply(verb+": "+err.Error()))
		return conformance.ExitError
	}
	var expires time.Time
	if req.ForS != nil {
		d := time.Duration(*req.ForS) * time.Second
		if d > MaxOverride {
			writeBody(stdout, errorReply(verb+": override capped at 24h; use --until-cleared"))
			return conformance.ExitError
		}
		expires = s.levels.cfg.Now().Add(d)
	}
	if err := s.levels.SetOverride(req.Resource, req.Level, req.Owner, expires); err != nil {
		writeBody(stdout, errorReply(verb+": "+err.Error()))
		return conformance.ExitError
	}
	reply := map[string]any{"schemaVersion": schemas.SchemaVersion, "resource": req.Resource, "level": req.Level, "until_cleared": req.UntilCleared}
	if !req.UntilCleared {
		reply["expires_at"] = expires.UTC().Format(time.RFC3339Nano)
	}
	return writeJSONReply(stdout, verb, reply)
}

func (s *Service) handleLevelClear(stdin io.Reader, stdout io.Writer) int {
	const verb = SubcommandLevelClear
	if s.noLevels(stdout, verb) {
		return conformance.ExitError
	}
	var req struct {
		Resource string `json:"resource"`
		All      bool   `json:"all"`
	}
	if !decodeVerb(stdin, stdout, verb, LevelClearRequestSchema, &req) {
		return conformance.ExitError
	}
	if (req.Resource == "") == !req.All {
		writeBody(stdout, errorReply(verb+": exactly one of resource and all is required"))
		return conformance.ExitError
	}
	targets := s.levels.OverrideResources()
	if !req.All {
		if _, ok := s.levels.cfg.Registry[req.Resource]; !ok {
			writeBody(stdout, errorReply(verb+": unknown resource "+req.Resource))
			return conformance.ExitError
		}
		targets = []string{req.Resource}
	}
	cleared, err := s.levels.ClearOverrides(targets)
	if err != nil {
		writeBody(stdout, errorReply(verb+": "+err.Error()))
		return conformance.ExitError
	}
	if cleared == nil {
		cleared = []string{}
	}
	return writeJSONReply(stdout, verb, map[string]any{"schemaVersion": schemas.SchemaVersion, "cleared": cleared})
}

func (s *Service) handleLevelRefresh(stdin io.Reader, stdout io.Writer) int {
	const verb = SubcommandLevelRefresh
	if s.levelRefresher == nil {
		writeBody(stdout, errorReply(verb+": level refresh is not available on this core"))
		return conformance.ExitError
	}
	var req struct {
		Resources []string `json:"resources"`
	}
	if !decodeVerb(stdin, stdout, verb, LevelRefreshRequestSchema, &req) {
		return conformance.ExitError
	}
	results := s.levelRefresher.RefreshLevels(context.Background(), req.Resources)
	rows := make([]map[string]any, 0, len(results))
	for _, r := range results {
		row := map[string]any{"resource": r.Resource, "outcome": r.Outcome}
		if r.Level != "" {
			row["level"] = r.Level
		}
		if r.Reason != "" {
			row["reason"] = r.Reason
		}
		rows = append(rows, row)
	}
	return writeJSONReply(stdout, verb, map[string]any{"schemaVersion": schemas.SchemaVersion, "results": rows})
}

var (
	_ = fmt.Sprintf
	_ = json.Marshal
)

package proto

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

// FromTree converts an aggregate.Tree (the daemon's internal state) into
// the wire DaemonState. baseline fields (now, uptime, version) must be
// filled by the caller; this function only handles Tree-derived fields.
func FromTree(tree *aggregate.Tree) *DaemonState {
	if tree == nil {
		return &DaemonState{}
	}
	d := &DaemonState{
		PlanCapUsd:    tree.PlanCapUSD,
		WeekCapUsd:    tree.WeekCapUSD,
		CcusageProbed: tree.CCUsageProbed,
	}
	if tree.CCUsageErr != nil {
		d.CcusageError = tree.CCUsageErr.Error()
	}
	if !tree.WindowResetsAt.IsZero() {
		d.WindowResetsAt = timestamppb.New(tree.WindowResetsAt)
	}
	for _, dir := range tree.Dirs {
		d.Dirs = append(d.Dirs, dirToProto(dir))
	}
	d.ActiveBlock = blockToProto(tree.ActiveBlock, tree.PlanCapUSD)
	d.ActiveWeek = weekToProto(tree.ActiveWeek, tree.WeekCapUSD)
	return d
}

func dirToProto(d *aggregate.Directory) *Directory {
	if d == nil {
		return nil
	}
	pd := &Directory{
		Path:         d.Path,
		Branch:       d.Branch,
		WorkingN:     uint32(d.WorkingN),
		IdleN:        uint32(d.IdleN),
		DormantN:     uint32(d.DormantN),
		TotalTokens:  uint64(d.TotalTokens),
		TotalCostUsd: d.TotalCostUSD,
		BurnRateSum:  d.BurnRateSum,
	}
	if d.PRInfo != nil {
		pd.PrInfo = &PRInfo{
			Number: uint32(d.PRInfo.Number),
			Title:  d.PRInfo.Title,
			Url:    d.PRInfo.URL,
		}
	}
	for _, sv := range d.Sessions {
		pd.Sessions = append(pd.Sessions, sessionViewToProto(sv))
	}
	return pd
}

func sessionViewToProto(sv *aggregate.SessionView) *SessionView {
	if sv == nil || sv.Session == nil {
		return nil
	}
	out := &SessionView{
		SessionId:     sv.SessionID,
		Pid:           uint32(sv.PID),
		Cwd:           sv.Cwd,
		Name:          sv.Name,
		Kind:          sv.Kind,
		Entrypoint:    sv.Entrypoint,
		Status:        session.Status(sv.Status).String(),
		Branch:        sv.Branch,
		TerminalHost:  sv.TerminalHost,
		ContextTokens: uint64(sv.ContextTokens),
		Model:         sv.Model,
		FirstPrompt:   sv.FirstPrompt,
		SubagentCount: uint32(sv.SubagentCount),
		SubshellCount: uint32(sv.SubshellCount),
		SessionTokens: uint64(sv.SessionTokens),
		BurnRateShort: sv.BurnRateShort,
		BurnRateLong:  sv.BurnRateLong,
		CostUsd:       sv.CostUSD,
		AwaitingInput: sv.AwaitingInput,
	}
	if !sv.StartedAt.IsZero() {
		out.StartedAt = timestamppb.New(sv.StartedAt)
	}
	if !sv.TranscriptMTime.IsZero() {
		out.TranscriptMtime = timestamppb.New(sv.TranscriptMTime)
	}
	if !sv.RateLimitResetsAt.IsZero() {
		out.RateLimitResetsAt = timestamppb.New(sv.RateLimitResetsAt)
	}
	// Nudge history: only emit on the wire when the session has actually
	// received a nudge. LastNudgeSources rides along even when empty so the
	// renderer can detect "fired but source unknown" gracefully.
	if !sv.LastNudgedAt.IsZero() {
		out.LastNudgedAt = timestamppb.New(sv.LastNudgedAt)
		out.LastNudgeSources = sv.LastNudgeSources
	}
	// Workspace tags from the session's env. Daemon-side privacy guard:
	// only forward known keys; never wire-expose arbitrary env.
	if sv.Env != nil {
		out.CmuxWorkspaceId = sv.Env["CMUX_WORKSPACE_ID"]
		out.TmuxSession = sv.Env["TMUX"]
		out.GcRig = sv.Env["GC_RIG"]
		out.GcAgent = sv.Env["GC_AGENT"]
		out.WorkspaceEnv = sv.Env["WORKSPACE"]
	}
	return out
}

func blockToProto(b *ccusage.Block, capUSD float64) *Block {
	if b == nil {
		return nil
	}
	pb := &Block{
		Id:                         b.StartTime.UTC().Format("2006-01-02T15Z"),
		IsActive:                   b.IsActive,
		CostUsd:                    b.CostUSD,
		TokensPerMinute:            b.BurnRate.TokensPerMinute,
		CostPerHour:                b.BurnRate.CostPerHour,
		ProjectionTotalCost:        b.Projection.TotalCost,
		ProjectionRemainingMinutes: uint32(b.Projection.RemainingMinutes),
	}
	if !b.StartTime.IsZero() {
		pb.StartTime = timestamppb.New(b.StartTime)
	}
	if !b.EndTime.IsZero() {
		pb.EndTime = timestamppb.New(b.EndTime)
	}
	if capUSD > 0 {
		pb.WindowPct = b.CostUSD / capUSD
	}
	if b.CapHitAt != nil {
		pb.CapHitAt = timestamppb.New(*b.CapHitAt)
	}
	return pb
}

func weekToProto(w *ccusage.WeeklyEntry, capUSD float64) *Week {
	if w == nil {
		return nil
	}
	pw := &Week{
		Period:  w.Period,
		CostUsd: w.TotalCost,
	}
	// ISO week ID — derive same as week.Tracker.
	if t, err := parseMonday(w.Period); err == nil {
		y, wk := t.ISOWeek()
		pw.Id = formatISOWeek(y, wk)
	}
	if capUSD > 0 {
		pw.WindowPct = w.TotalCost / capUSD
	}
	if w.CapHitAt != nil {
		pw.CapHitAt = timestamppb.New(*w.CapHitAt)
	}
	return pw
}

// SessionDetailFromView converts an aggregate.SessionView into a
// SessionDetail proto, populating LastError and PendingNudge from
// the view's enrichment fields.
//
// The caller is responsible for setting LabelPairs and any other
// detail-level fields that are not derived from the session view.
func SessionDetailFromView(sv *aggregate.SessionView) *SessionDetail {
	if sv == nil {
		return nil
	}
	out := &SessionDetail{
		View: sessionViewToProto(sv),
	}
	if sv.LastError != nil {
		out.LastError = apiErrorToProto(sv.LastError, sv.LastErrorRetryable)
	}
	if sv.PendingNudge != nil {
		out.PendingNudge = &PendingNudge{Sources: sv.PendingNudge.Sources}
	}
	return out
}

// apiErrorToProto serializes an ErrorRecord. retryable is pa-monitor's
// escalation-aware auto-resume verdict (tracked on the SessionView, not the
// shared record): the daemon flips it to false on escalation.
func apiErrorToProto(e *transcript.ErrorRecord, retryable bool) *ApiError {
	if e == nil {
		return nil
	}
	out := &ApiError{
		Kind:         string(e.Kind),
		Text:         e.Text,
		IsTerminal:   e.IsTerminal,
		IsRetryable:  retryable,
		FromSubagent: e.FromSubagent,
	}
	if !e.At.IsZero() {
		out.At = timestamppb.New(e.At)
	}
	return out
}

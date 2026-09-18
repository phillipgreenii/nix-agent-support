// status_json.go: the --json sibling of runStatus's text output, plus the
// small helpers status/info/search all share for flag parsing and
// dial/GetState boilerplate. Emits exactly the data runStatus already
// gathers (state.GetDirs() sessions + the per-session GetSessionInfo
// details it collects for the annotation table, plus
// ActiveBlock/ActiveWeek) as one JSON document. No new gRPC calls.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/rpcclient"
)

// stripJSONFlag removes a "--json" token from args (wherever it appears —
// never assumed to be in a fixed position relative to a positional
// selector/query) and reports whether it was present, returning the
// remaining args in their original relative order. Shared by
// status/info/search.
func stripJSONFlag(args []string) (rest []string, jsonMode bool) {
	for _, a := range args {
		if a == "--json" {
			jsonMode = true
			continue
		}
		rest = append(rest, a)
	}
	return rest, jsonMode
}

// contextWithTimeout returns the standard 3s-timeout context every
// dial-the-daemon subcommand uses (matches runStatus/runInfo/
// runCaffeinate's existing inline 3*time.Second calls).
func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

// dialOrExit dials the daemon, printing the standard unreachable message
// and exiting 2 on failure (matches runStatus's original inline block).
// The returned error is always nil on return — os.Exit already terminated
// the process on failure — callers still check it defensively to satisfy
// Go's control-flow expectations, mirroring this file's own
// getStateOrExit below.
func dialOrExit(ctx context.Context) (*rpcclient.Client, error) {
	client, err := rpcclient.Dial(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, rpcclient.DaemonUnavailableMessage("<unknown>"))
		os.Exit(2)
	}
	return client, nil
}

// getStateOrExit calls GetState, printing a diagnostic and exiting 2 on
// failure (matches runStatus's original inline block).
func getStateOrExit(ctx context.Context, client *rpcclient.Client) (*pb.DaemonState, error) {
	state, err := client.C.GetState(ctx, &pb.GetStateRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: GetState: %v\n", err)
		os.Exit(2)
	}
	return state, nil
}

// sessionJSON is the --json wire shape for one session — reused verbatim
// by `info --json` for a session selector, and unmarshaled verbatim
// (re-declared) by pg-connector-agentsession-pa-monitor. Field names here
// are the wire contract; changing one is a breaking change for both
// consumers.
type sessionJSON struct {
	SessionID     string  `json:"session_id"`
	Pid           *int    `json:"pid,omitempty"`
	Cwd           string  `json:"cwd"`
	Name          string  `json:"name,omitempty"`
	Model         string  `json:"model"`
	Status        string  `json:"status"`
	Blocker       string  `json:"blocker,omitempty"`
	Branch        string  `json:"branch,omitempty"`
	TerminalHost  string  `json:"terminal_host,omitempty"`
	StartedAt     string  `json:"started_at,omitempty"`
	SessionTokens uint64  `json:"session_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	LongIdle      bool    `json:"long_idle"`
}

type usageWindowJSON struct {
	ID       string  `json:"id"`
	CostUSD  float64 `json:"cost_usd"`
	CapHitAt *string `json:"cap_hit_at,omitempty"`
}

type statusJSONDoc struct {
	Sessions    []sessionJSON    `json:"sessions"`
	ActiveBlock *usageWindowJSON `json:"active_block,omitempty"`
	ActiveWeek  *usageWindowJSON `json:"active_week,omitempty"`
}

// toSessionJSON converts one SessionView into the wire shape. now is
// injected for testability.
func toSessionJSON(v *pb.SessionView, now time.Time) sessionJSON {
	sj := sessionJSON{
		SessionID:     v.GetSessionId(),
		Cwd:           v.GetCwd(),
		Name:          v.GetName(),
		Model:         v.GetModel(),
		Status:        v.GetStatus(),
		Blocker:       v.GetBlocker(),
		Branch:        v.GetBranch(),
		TerminalHost:  v.GetTerminalHost(),
		SessionTokens: v.GetSessionTokens(),
		CostUSD:       v.GetCostUsd(),
	}
	if pid := v.GetPid(); pid != 0 {
		p := int(pid)
		sj.Pid = &p
	}
	if ts := v.GetStartedAt(); ts != nil {
		sj.StartedAt = ts.AsTime().UTC().Format(time.RFC3339)
	}
	if ts := v.GetTranscriptMtime(); ts != nil {
		sj.LongIdle = session.IsLongIdle(now, ts.AsTime(), session.LongIdleThreshold)
	}
	return sj
}

func toUsageWindowJSON(id string, costUSD float64, capHitAt *timestamppb.Timestamp) *usageWindowJSON {
	uw := &usageWindowJSON{ID: id, CostUSD: costUSD}
	if capHitAt != nil {
		s := capHitAt.AsTime().UTC().Format(time.RFC3339)
		uw.CapHitAt = &s
	}
	return uw
}

// statusJSON builds the full --json document from state (as GetState
// returned it) and now (injected for testability; production callers pass
// time.Now().UTC()). details is currently unused by the JSON path (the
// text path's LastError/PendingNudge annotations have no --json
// equivalent yet — out of scope for this task) but is accepted so a
// future extension does not need to change this function's signature.
func statusJSON(state *pb.DaemonState, details []*pb.SessionDetail, now time.Time) statusJSONDoc {
	var doc statusJSONDoc
	for _, d := range state.GetDirs() {
		for _, v := range d.GetSessions() {
			if v.GetSessionId() == "" {
				continue
			}
			doc.Sessions = append(doc.Sessions, toSessionJSON(v, now))
		}
	}
	if b := state.GetActiveBlock(); b != nil {
		doc.ActiveBlock = toUsageWindowJSON(b.GetId(), b.GetCostUsd(), b.GetCapHitAt())
	}
	if w := state.GetActiveWeek(); w != nil {
		doc.ActiveWeek = toUsageWindowJSON(w.GetId(), w.GetCostUsd(), w.GetCapHitAt())
	}
	return doc
}

func writeStatusJSON(w io.Writer, state *pb.DaemonState, details []*pb.SessionDetail) error {
	enc := json.NewEncoder(w)
	return enc.Encode(statusJSON(state, details, time.Now().UTC()))
}

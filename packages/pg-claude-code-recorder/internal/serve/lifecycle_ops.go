package serve

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/lifecycle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

func decodeBody(body []byte, v any) *wire.Error {
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return wire.Errf(wire.ErrBadRequest, "request: "+err.Error())
	}
	return nil
}

func lifeErr(err error, what string) *wire.Error {
	if errors.Is(err, lifecycle.ErrNotFound) {
		return wire.Errf(wire.ErrNotFound, what)
	}
	return wire.FromErr(err)
}

// opConversationShow answers one Conversation: its state and stage, its end
// evidence (never a label) and its Process history.
func (d *Daemon) opConversationShow(ctx context.Context, body []byte) (any, *wire.Error) {
	var req lifecycle.ConversationShow
	if werr := decodeBody(body, &req); werr != nil {
		return nil, werr
	}
	if req.SessionID == "" {
		return nil, wire.Errf(wire.ErrBadRequest, "conversation.show needs a session_id")
	}
	c, err := d.eng.Show(ctx, req.SessionID)
	if err != nil {
		return nil, lifeErr(err, "no Conversation "+req.SessionID)
	}
	return c, nil
}

// opConversationList answers the Conversations whose evidence matches the
// filters (a superset of the rows a label can come from), newest first.
func (d *Daemon) opConversationList(ctx context.Context, body []byte) (any, *wire.Error) {
	var req lifecycle.ConversationFilter
	if werr := decodeBody(body, &req); werr != nil {
		return nil, werr
	}
	cs, err := d.eng.List(ctx, req)
	if err != nil {
		return nil, wire.Errf(wire.ErrBadRequest, err.Error())
	}
	if cs == nil {
		cs = []lifecycle.Conversation{}
	}
	return lifecycle.ConversationList{Conversations: cs, Degraded: !d.backfillDone.Load()}, nil
}

func (d *Daemon) opProcessShow(ctx context.Context, body []byte) (any, *wire.Error) {
	var req lifecycle.ProcessShow
	if werr := decodeBody(body, &req); werr != nil {
		return nil, werr
	}
	if req.Pid <= 0 {
		return nil, wire.Errf(wire.ErrBadRequest, "process.show needs a pid")
	}
	p, err := d.eng.Process(ctx, req.Pid, req.ProcStart)
	if err != nil {
		return nil, lifeErr(err, "no Process with that pid")
	}
	return p, nil
}

func (d *Daemon) opProcessList(ctx context.Context, body []byte) (any, *wire.Error) {
	var req lifecycle.ProcessFilter
	if werr := decodeBody(body, &req); werr != nil {
		return nil, werr
	}
	ps, err := d.eng.Processes(ctx, req)
	if err != nil {
		return nil, wire.FromErr(err)
	}
	if ps == nil {
		ps = []lifecycle.Process{}
	}
	return lifecycle.ProcessList{Processes: ps, Degraded: !d.backfillDone.Load()}, nil
}

package serve

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/settle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// The requests and answers of the held, where, remove, extract and doctor
// operations.
type (
	// HeldRequest is the held body; Retry re-runs the check of every held
	// Conversation first.
	HeldRequest struct {
		Retry bool `json:"retry,omitempty"`
	}
	// HeldAnswer lists the held Conversations (after the retry, when asked).
	HeldAnswer struct {
		Held    []settle.HeldRow `json:"held"`
		Retried int              `json:"retried"`
		Cleared int              `json:"cleared"`
	}
	// WhereRequest names the Conversation.
	WhereRequest struct {
		Session string `json:"session"`
	}
	// RemoveRequest is the remove body.
	RemoveRequest struct {
		Sessions []string `json:"sessions,omitempty"`
		DryRun   bool     `json:"dry_run,omitempty"`
	}
	// ExtractRequest is the extract body; To is "" for the Claude directory.
	ExtractRequest struct {
		Session string `json:"session"`
		To      string `json:"to,omitempty"`
	}
)

func (d *Daemon) opHeld(_ context.Context, body []byte) (any, *wire.Error) {
	var req HeldRequest
	if werr := decodeBody(body, &req); werr != nil {
		return nil, werr
	}
	var ans HeldAnswer
	if req.Retry {
		before, _ := d.sett.Held()
		outs, err := d.sett.RecheckHeld()
		if err != nil {
			return nil, wire.Errf(wire.ErrInternal, err.Error())
		}
		ans.Retried = len(outs)
		after, _ := d.sett.Held()
		ans.Cleared = len(before) - len(after)
		if ans.Cleared < 0 {
			ans.Cleared = 0
		}
	}
	held, err := d.sett.Held()
	if err != nil {
		return nil, wire.Errf(wire.ErrInternal, err.Error())
	}
	ans.Held = held
	return ans, nil
}

func (d *Daemon) opWhere(_ context.Context, body []byte) (any, *wire.Error) {
	var req WhereRequest
	if werr := decodeBody(body, &req); werr != nil {
		return nil, werr
	}
	if req.Session == "" {
		return nil, wire.Errf(wire.ErrBadRequest, "where needs a session")
	}
	w, err := d.sett.Where(req.Session)
	if errors.Is(err, store.ErrNotFound) {
		return nil, wire.Errf(wire.ErrNotFound, "no Conversation "+req.Session)
	}
	if err != nil {
		return nil, wire.Errf(wire.ErrInternal, err.Error())
	}
	return w, nil
}

func (d *Daemon) opRemove(_ context.Context, body []byte) (any, *wire.Error) {
	var req RemoveRequest
	if werr := decodeBody(body, &req); werr != nil {
		return nil, werr
	}
	rep, err := d.sett.Remove(settle.RemoveOptions{Sessions: req.Sessions, DryRun: req.DryRun})
	switch {
	case errors.Is(err, settle.ErrRemovalDisabled):
		return nil, wire.Errf(wire.ErrRemovalDisabled, err.Error()+"; remove --dry-run shows what would go")
	case err != nil && len(rep.Removed) == 0:
		return nil, wire.Errf(wire.ErrInternal, err.Error())
	}
	return rep, nil
}

func (d *Daemon) opExtract(_ context.Context, body []byte) (any, *wire.Error) {
	var req ExtractRequest
	if werr := decodeBody(body, &req); werr != nil {
		return nil, werr
	}
	if req.Session == "" {
		return nil, wire.Errf(wire.ErrBadRequest, "extract needs a session")
	}
	res, err := d.sett.Extract(req.Session, req.To)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, wire.Errf(wire.ErrNotFound, "no settled Conversation "+req.Session+" to extract")
	case errors.Is(err, settle.ErrTargetExists):
		return nil, wire.Errf(wire.ErrTargetExists, err.Error())
	case errors.Is(err, settle.ErrHeldByLive):
		return nil, wire.Errf(wire.ErrHeldByLive, err.Error())
	case errors.Is(err, settle.ErrBadTarget):
		return nil, wire.Errf(wire.ErrBadRequest, err.Error())
	case err != nil:
		return nil, wire.Errf(wire.ErrInternal, err.Error())
	}
	return res, nil
}

func (d *Daemon) opDoctor(_ context.Context, _ []byte) (any, *wire.Error) {
	res, err := d.sett.Doctor()
	if err != nil {
		return nil, wire.Errf(wire.ErrInternal, err.Error())
	}
	return res, nil
}

// doctorLoop runs doctor from a timer inside the daemon (doctor.interval), so no
// external scheduler is needed. The first run comes one interval after start,
// or sooner when the last result is older than the interval.
func (d *Daemon) doctorLoop() {
	defer d.refreshing.Done()
	every := d.cfg.DoctorInterval
	if every <= 0 {
		return
	}
	wait := every
	if last, ok := settle.LoadDoctor(d.cfg.StateDir); ok {
		if age := d.clock.Now().Sub(time.Unix(0, last.AtNS)); age < every {
			wait = every - age
		} else {
			wait = time.Minute
		}
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
			if _, err := d.sett.Doctor(); err != nil {
				d.obs.Log.Warn("doctor_failed", "err", err.Error())
			}
			t.Reset(every)
		}
	}
}

// loadDoctorGauges exports the last doctor result from its file, so a restart
// does not reset doctor_last_run_timestamp to "never".
func (d *Daemon) loadDoctorGauges() {
	if last, ok := settle.LoadDoctor(d.cfg.StateDir); ok {
		d.obs.Reg.Set(obs.MDoctorLastRun, nil, float64(last.AtNS)/1e9)
		d.obs.Reg.Set(obs.MDoctorMismatchFiles, nil, float64(last.MismatchFiles))
	}
}

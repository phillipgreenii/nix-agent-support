package serve

import (
	"context"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/settle"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/wire"
)

// busyOps is an OpsPort whose where and remove give up on the run lock.
type busyOps struct{ OpsPort }

func (busyOps) WhereCtx(context.Context, string) (settle.WhereInfo, error) {
	return settle.WhereInfo{}, settle.ErrRunBusy
}

func (busyOps) RemoveCtx(context.Context, settle.RemoveOptions) (settle.RemoveReport, error) {
	return settle.RemoveReport{}, settle.ErrRunBusy
}

// A where or remove that gave up waiting for the run lock is answered busy (the
// retryable outcome), not internal.
func TestWhereAndRemoveAnswerBusyWhenTheRunLockIsHeld(t *testing.T) {
	d := &Daemon{ops: busyOps{}}
	if _, werr := d.opWhere(context.Background(), []byte(`{"session":"s"}`)); werr == nil || werr.Name != wire.ErrBusy {
		t.Fatalf("where: %+v, want busy", werr)
	}
	if _, werr := d.opRemove(context.Background(), []byte(`{}`)); werr == nil || werr.Name != wire.ErrBusy {
		t.Fatalf("remove: %+v, want busy", werr)
	}
}

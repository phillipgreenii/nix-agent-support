package notice

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Op names.
const (
	OpWriteBegin = "write_begin"
	OpInvalidate = "invalidate"
)

// Ops lists the ops Handlers returns, for the assembly's drift guard.
var Ops = []string{OpWriteBegin, OpInvalidate}

// Deps wires the handlers.
type Deps struct {
	// Table is the hold table the GuardStore reads.
	Table *Table
	// Summary and Detail are the services whose groups an invalidate re-pulls.
	Summary *summary.Service
	Detail  *detail.Service
	// Store is the RAW daemon store (not the GuardStore): the generation bumps
	// go through it, and it tells a known issue from an unknown one.
	Store *store.Store
	// Queries queues one re-run of every configured query and returns the names
	// it queued; nil means none. It is called only for an issue the daemon does
	// not know.
	Queries func() []string
	// Clock is the time source; nil means the system clock.
	Clock governor.Clock
}

var errNotWired = errors.New("notice: the daemon has not wired the write-notice handlers")

func (d Deps) ready() error {
	if d.Table == nil || d.Store == nil {
		return errNotWired
	}
	return nil
}

func (d Deps) now() governor.Clock {
	if d.Clock == nil {
		return governor.SystemClock{}
	}
	return d.Clock
}

func (d Deps) puller() puller {
	return puller{st: d.Store, sum: d.Summary, det: d.Detail, queries: d.Queries}
}

// Handlers returns exactly write_begin and invalidate, each at
// schema.IssueSchemaVersion. Both answer a null result, make no origin call and
// never wait for the daemon to be idle.
func Handlers(d Deps) map[string]scriptout.OpHandler {
	return map[string]scriptout.OpHandler{
		OpWriteBegin: {SchemaVersion: schema.IssueSchemaVersion, Handle: d.writeBegin},
		OpInvalidate: {SchemaVersion: schema.IssueSchemaVersion, Handle: d.invalidate},
	}
}

type noticeArgs struct {
	ID       string `json:"id"`
	Instance string `json:"instance"`
}

func decode(op string, args json.RawMessage) (noticeArgs, error) {
	var a noticeArgs
	if err := scriptout.Decode(args, &a); err != nil {
		return a, scriptout.WrapError(scriptout.ErrInvalidArgument, "decode "+op+" args: "+err.Error())
	}
	return a, nil
}

func checkID(id string) error {
	if !internal.IsJiraKey(id) {
		return scriptout.WrapError(scriptout.ErrInvalidArgument, "not a Jira issue key: "+id)
	}
	return nil
}

// writeBegin holds the entity for the lease and bumps the generation of both its
// groups. The hold comes FIRST: a commit that checks the hold after the bump
// then always sees it, so no refresh that took its token after the bump can
// store the content it fetched before the write.
func (d Deps) writeBegin(ctx context.Context, args json.RawMessage) (any, error) {
	a, err := decode(OpWriteBegin, args)
	if err != nil {
		return nil, err
	}
	if (a.ID == "") == (a.Instance == "") {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "write_begin takes exactly one of id and instance")
	}
	if err := d.ready(); err != nil {
		return nil, err
	}
	if a.Instance != "" {
		d.Table.Begin(ctx, instanceKey(a.Instance), d.Table.Lease())
		return nil, nil
	}
	if err := checkID(a.ID); err != nil {
		return nil, err
	}
	id := groups.FoldRef(a.ID)
	d.Table.Begin(ctx, id, d.Table.Lease())
	if err := bumpBoth(ctx, d.Store, d.now().Now(), id); err != nil {
		return nil, err
	}
	return nil, nil
}

// invalidate bumps the generations again (so a refresh begun before the call
// loses), releases the hold and queues one re-pull. The bump comes BEFORE the
// release: a refresh that took its token while the hold was in force still
// loses its commit, and one that commits after the release has fetched after the
// write.
func (d Deps) invalidate(ctx context.Context, args json.RawMessage) (any, error) {
	a, err := decode(OpInvalidate, args)
	if err != nil {
		return nil, err
	}
	if a.ID == "" && a.Instance == "" {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "invalidate takes an id")
	}
	if err := d.ready(); err != nil {
		return nil, err
	}
	if a.Instance != "" {
		d.Table.Release(ctx, instanceKey(a.Instance))
	}
	if a.ID == "" {
		return nil, nil
	}
	if err := checkID(a.ID); err != nil {
		return nil, err
	}
	id := groups.FoldRef(a.ID)
	if err := bumpBoth(ctx, d.Store, d.now().Now(), id); err != nil {
		return nil, err
	}
	d.Table.Release(ctx, id)
	if err := d.puller().queue(ctx, id); err != nil {
		return nil, err
	}
	return nil, nil
}

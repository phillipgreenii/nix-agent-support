package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	jira "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/issue"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The two write notices the client sends the daemon around a write; they are
// the ops of the daemon's notice package (a test holds the names equal).
const (
	noticeBegin      = "write_begin"
	noticeInvalidate = "invalidate"
)

// writeTable is the five writes of the old dispatch table over backend, the
// very handlers the stateless backend served, so a write answers byte for byte
// as it did before the daemon.
func writeTable(backend issue.Provider) scriptout.DispatchTable {
	all := issue.NewDispatchTable(backend)
	table := scriptout.DispatchTable{}
	for _, op := range WriteOps {
		table[op] = all[op]
	}
	return table
}

// runWrite performs one write directly against the origin tool in this
// process, under the caller's own environment, and tells the daemon about it
// (INV-ICD-3): write_begin before and invalidate after, each bounded by the
// notice delivery timeout. The write never depends on the daemon: a notice that
// is not delivered never fails the write and never delays it by more than its
// timeout, and when write_begin is not delivered the write still runs and
// invalidate is skipped.
func runWrite(ctx context.Context, o Options, raw json.RawMessage, req scriptout.Request, stdout io.Writer) int {
	backend := o.Backend
	if backend == nil {
		backend = jira.New(jira.NewCLIRunner())
	}
	n := notifier{o: o, timeout: noticeTimeout(o.Config)}

	// A create has no id before it runs, so it holds the instance name; the
	// other writes hold the entity. A request without an id (a malformed
	// write) tells the daemon nothing and fails on its own.
	id := requestID(req)
	var begun bool
	switch {
	case req.Op == OpCreate:
		begun = n.send(ctx, noticeBegin, map[string]string{"instance": o.Config.Instance})
	case id != "":
		begun = n.send(ctx, noticeBegin, map[string]string{"id": id})
	}

	var out bytes.Buffer
	code := scriptout.ServeOne(writeTable(backend), bytes.NewReader(raw), &out)

	if begun {
		switch {
		case req.Op != OpCreate:
			n.send(ctx, noticeInvalidate, map[string]string{"id": id})
		case createdKey(out.Bytes()) != "":
			n.send(ctx, noticeInvalidate, map[string]string{"id": createdKey(out.Bytes())})
		default:
			// The create failed, so there is no key to invalidate; releasing the
			// instance hold now saves the daemon waiting out its lease.
			n.send(ctx, noticeInvalidate, map[string]string{"instance": o.Config.Instance})
		}
	}

	if _, err := stdout.Write(out.Bytes()); err != nil {
		return 1
	}
	return code
}

// noticeTimeout is the delivery timeout of one notice: write_notice_timeout.
func noticeTimeout(cfg *config.Config) time.Duration {
	if d := cfg.WriteNoticeTimeout.Std(); d > 0 {
		return d
	}
	return config.DefaultWriteNoticeTimeout
}

// requestID is the id the request's args name, "" when there is none.
func requestID(req scriptout.Request) string {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Args, &a); err != nil {
		return ""
	}
	return a.ID
}

// createdKey is the id of the issue a successful create response carries, ""
// for a failed create or an unreadable answer.
func createdKey(out []byte) string {
	var resp struct {
		Error  *scriptout.Error `json:"error"`
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || resp.Error != nil {
		return ""
	}
	return resp.Result.ID
}

// notifier delivers the write notices.
type notifier struct {
	o       Options
	timeout time.Duration
}

// send delivers one notice and reports whether the daemon acknowledged it. The
// whole delivery, connecting included, is bounded by the timeout; a failure is
// logged and never surfaced to the write.
func (n notifier) send(ctx context.Context, op string, args map[string]string) bool {
	rawArgs, err := json.Marshal(args)
	if err != nil {
		return false
	}
	reply, err := socketClient(n.o).Call(ctx, RouterInstance, scriptout.Request{Op: op, Args: rawArgs}, n.timeout, n.timeout)
	if err != nil {
		n.log(op, err.Error())
		return false
	}
	var resp scriptout.Response
	if err := json.Unmarshal(reply.Response, &resp); err != nil {
		n.log(op, "the daemon's answer is unreadable: "+err.Error())
		return false
	}
	if resp.Error != nil {
		n.log(op, resp.Error.Code+": "+resp.Error.Message)
		return false
	}
	return true
}

func (n notifier) log(op, msg string) {
	if n.o.Logger != nil {
		n.o.Logger.Log("write_notice_failed", "op", op, "error", msg)
	}
}

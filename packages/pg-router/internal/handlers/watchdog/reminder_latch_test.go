package watchdog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/handlers/ccpool"
	"github.com/phillipgreenii/pg-router/internal/handlers/usage"
)

// metaCC is a fakeCC that also answers metadata reads (ccpool.MetaReader) and
// records the metadata it is asked to write.
type metaCC struct {
	fakeCC
	meta     map[string]string
	getErrs  []error // consumed one per GetMeta call; nil entry or exhausted = success
	getCalls int
	setErr   error
	writes   map[string]string
	order    []string // "send" / "set:<key>"
}

func (m *metaCC) GetMeta(context.Context, string) (map[string]string, error) {
	i := m.getCalls
	m.getCalls++
	if i < len(m.getErrs) && m.getErrs[i] != nil {
		return nil, m.getErrs[i]
	}
	out := map[string]string{}
	for k, v := range m.meta {
		out[k] = v
	}
	return out, nil
}

func (m *metaCC) Send(ctx context.Context, n, p string, mode ccpool.SendMode) error {
	m.order = append(m.order, "send")
	return m.fakeCC.Send(ctx, n, p, mode)
}

func (m *metaCC) SetMeta(_ context.Context, _, key, val string) error {
	m.order = append(m.order, "set:"+key)
	if m.setErr != nil {
		return m.setErr
	}
	if m.writes == nil {
		m.writes = map[string]string{}
	}
	m.writes[key] = val
	return nil
}

func newMetaCC(meta map[string]string) *metaCC {
	return &metaCC{
		fakeCC: fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}},
		meta:   meta,
	}
}

// reminderRun drives a watchdog through the reminder level (730/1000) and then
// the hard stop, so Run terminates.
func reminderRun(cc ccpool.Runner, gen int64) *Watchdog {
	r := &fakeReader{seq: []usage.Snapshot{{OutputTokens: 730}, {OutputTokens: 730}, {OutputTokens: 1000}}}
	wd := newWD(r, cc, &recBD{}, tokBudget(1000))
	wd.OwnerID, wd.Generation = "disp-a", gen
	return wd
}

func queued(cc *metaCC) int {
	n := 0
	for _, s := range cc.sent {
		if strings.Contains(s, "near limit") {
			n++
		}
	}
	return n
}

func TestReminderLatch_pastesThenWritesLatch(t *testing.T) {
	cc := newMetaCC(map[string]string{ccpool.MetaKeyLeaseGeneration: "1"})
	_ = reminderRun(cc, 1).Run(context.Background(), "s", "b")
	if queued(cc) != 1 {
		t.Fatalf("want one reminder, got %v", cc.sent)
	}
	if len(cc.order) < 2 || cc.order[0] != "send" || cc.order[1] != "set:"+ccpool.MetaKeyReminderSent {
		t.Fatalf("paste must precede the latch write, got %v", cc.order)
	}
	if _, ok := ccpool.ParseMetaTime(cc.writes[ccpool.MetaKeyReminderSent]); !ok {
		t.Errorf("latch must be an RFC 3339 instant, got %q", cc.writes[ccpool.MetaKeyReminderSent])
	}
}

func TestReminderLatch_restartWithLatchSetDoesNotPaste(t *testing.T) {
	at := ccpool.FormatMetaTime(time.Unix(100, 0))
	cc := newMetaCC(map[string]string{ccpool.MetaKeyLeaseGeneration: "1", ccpool.MetaKeyReminderSent: at})
	_ = reminderRun(cc, 1).Run(context.Background(), "s", "b")
	if queued(cc) != 0 {
		t.Errorf("a restart past the threshold with the latch set must not paste, got %v", cc.sent)
	}
	if cc.writes[ccpool.MetaKeyReminderSent] != "" {
		t.Error("must not rewrite the latch")
	}
	if cc.cancels == 0 {
		t.Error("the cancel and hard levels keep working")
	}
}

func TestReminderLatch_emptyOrUnparsableReadsUnset(t *testing.T) {
	for _, v := range []string{"", "garbage"} {
		cc := newMetaCC(map[string]string{ccpool.MetaKeyReminderSent: v})
		_ = reminderRun(cc, 1).Run(context.Background(), "s", "b")
		if queued(cc) != 1 {
			t.Errorf("value %q must read as unset and paste once, got %v", v, cc.sent)
		}
	}
}

func TestReminderLatch_supersededDoesNotPaste(t *testing.T) {
	cc := newMetaCC(map[string]string{ccpool.MetaKeyLeaseGeneration: "2"})
	_ = reminderRun(cc, 1).Run(context.Background(), "s", "b")
	if queued(cc) != 0 {
		t.Errorf("superseded watchdog must not paste, got %v", cc.sent)
	}
	if cc.writes[ccpool.MetaKeyReminderSent] != "" {
		t.Error("superseded watchdog must not latch")
	}
}

func TestReminderLatch_absentOrUnparsableGenerationIsNeverSuperseded(t *testing.T) {
	for _, meta := range []map[string]string{{}, {ccpool.MetaKeyLeaseGeneration: "x"}} {
		cc := newMetaCC(meta)
		_ = reminderRun(cc, 0).Run(context.Background(), "s", "b")
		if queued(cc) != 1 {
			t.Errorf("meta %v: want one reminder, got %v", meta, cc.sent)
		}
	}
}

func TestReminderLatch_neverAdoptsNewerGeneration(t *testing.T) {
	cc := newMetaCC(map[string]string{ccpool.MetaKeyLeaseGeneration: "5", ccpool.MetaKeyLeaseOwner: "disp-b"})
	wd := reminderRun(cc, 7)
	_ = wd.Run(context.Background(), "s", "b")
	if wd.Generation != 7 || wd.OwnerID != "disp-a" {
		t.Errorf("owner/generation must stay as constructed, got %q %d", wd.OwnerID, wd.Generation)
	}
}

func TestReminderLatch_failedReReadSkipsWithoutLatchingThenRetries(t *testing.T) {
	// call 0 is the start read; call 1 is the pre-paste re-read (fails); call 2 succeeds.
	cc := newMetaCC(map[string]string{ccpool.MetaKeyLeaseGeneration: "1"})
	cc.getErrs = []error{nil, errors.New("meta list: boom")}
	_ = reminderRun(cc, 1).Run(context.Background(), "s", "b")
	if queued(cc) != 1 {
		t.Fatalf("the paste must be retried on the next poll and sent once, got %v", cc.sent)
	}
	if cc.getCalls < 3 {
		t.Errorf("want a retry re-read, got %d GetMeta calls", cc.getCalls)
	}
}

func TestReminderLatch_failedLatchWriteKeepsInMemoryLatch(t *testing.T) {
	cc := newMetaCC(map[string]string{ccpool.MetaKeyLeaseGeneration: "1"})
	cc.setErr = errors.New("meta set: disk I/O error")
	_ = reminderRun(cc, 1).Run(context.Background(), "s", "b")
	if queued(cc) != 1 {
		t.Errorf("a failed latch write must not repeat the reminder, got %v", cc.sent)
	}
}

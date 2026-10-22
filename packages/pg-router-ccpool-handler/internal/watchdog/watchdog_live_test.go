package watchdog

import (
	"context"
	"errors"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/usage"
)

// INV-CCH-38: the reminder and the wrap-up are sent through ccpool reply, which
// resumes a cold session with an EMPTY environment, so each first checks that the
// session is live and treats "not live" as ended (the send is skipped and the
// wait loop's dead-session handling classifies the end).

func rampReader() *fakeReader {
	return &fakeReader{seq: []usage.Snapshot{{OutputTokens: 730}, {OutputTokens: 920}, {OutputTokens: 1000}}}
}

func TestRun_deadSession_reminderAndWrapUpAreSkipped(t *testing.T) {
	for name, list := range map[string][]ccpool.Session{
		"row not live": {{ExternalID: "s", Live: false, CWD: "/repo"}},
		"row absent":   {},
	} {
		t.Run(name, func(t *testing.T) {
			cc := &fakeCC{list: list}
			wd := newWD(rampReader(), cc, &recBD{}, tokBudget(1000))
			wd.FirstTurnStarted = func(string) bool { return true }

			err := wd.Run(context.Background(), "s", "zr-1")

			if !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("the hard stop still fires, got %v", err)
			}
			if len(cc.sent) != 0 {
				t.Fatalf("nothing may be sent to a session that is not live; sent=%v", cc.sent)
			}
		})
	}
}

// failAfterListCC reports the session live to every List, then fails every Send:
// the session dies between the liveness check and the send.
type failAfterListCC struct {
	*fakeCC
	sends int
}

func (f *failAfterListCC) Send(context.Context, string, string, ccpool.SendMode) error {
	f.sends++
	return errors.New("ccpool reply: session gone")
}

func TestRun_sessionDiesBetweenCheckAndSend_resultIgnored(t *testing.T) {
	cc := &failAfterListCC{fakeCC: &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}}
	wd := newWD(rampReader(), cc, &recBD{}, tokBudget(1000))
	wd.FirstTurnStarted = func(string) bool { return true }

	err := wd.Run(context.Background(), "s", "zr-1")

	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("a failed send is ignored, as it always was; the hard stop still fires: %v", err)
	}
	if cc.sends != 2 {
		t.Fatalf("the reminder and the wrap-up were both attempted against a session reported live; sends=%d", cc.sends)
	}
}

func TestRun_liveSession_reminderAndWrapUpSent(t *testing.T) {
	cc := &fakeCC{list: []ccpool.Session{{ExternalID: "s", Live: true, CWD: "/repo"}}}
	wd := newWD(rampReader(), cc, &recBD{}, tokBudget(1000))
	wd.FirstTurnStarted = func(string) bool { return true }

	_ = wd.Run(context.Background(), "s", "zr-1")

	if len(cc.sent) != 2 {
		t.Fatalf("the reminder and the wrap-up are both sent to a live session; sent=%v", cc.sent)
	}
}

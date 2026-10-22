package orchestrator

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// TestRoleBusy_DerivedFromDeclineKindNeverFromReason pins GOAL-MIN-1 for the busy
// state (bead pg2-8ij5o): a real roleListener feeds a real metrics.Emitter, and
// whether the role reads busy after one decline depends on the decline's KIND
// alone. The reason strings are chosen to read as every kind (and as unrelated
// text), in both directions: a reason that says "busy" on a neutral or fault
// decline does not make the role busy, and a reason that says "neutral" or
// "fault" on a busy decline does not stop it.
func TestRoleBusy_DerivedFromDeclineKindNeverFromReason(t *testing.T) {
	reasons := []string{"", "busy", "at-capacity", "neutral", "fault", "unavailable", "launch-failed", "anything at all"}
	for _, kind := range []string{"busy", "", "unknown-kind", "fault", "neutral"} {
		wantBusy := kind != "fault" && kind != "neutral"
		for _, reason := range reasons {
			t.Run("kind="+kind+"/reason="+reason, func(t *testing.T) {
				e, err := metrics.New(sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader())),
					func() map[string]int { return nil }, metrics.WithRoleProgress("desk-pr"))
				if err != nil {
					t.Fatalf("metrics.New: %v", err)
				}
				o := newOrch(fastCfg(t), testQuerySet(nil, nil))
				o.Handler = &seqHandler{errs: []error{&wireclient.BusyDecline{Reason: reason, Kind: kind}}}
				o.RoleProgressObserver = e
				l := o.NewListener(context.Background(), roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}})

				if got := l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")}); got.Accepted || got.Decline != eventqueue.DeclineBusy {
					t.Fatalf("Offer() = %+v, want a pre-accept busy decline", got)
				}
				if got := e.RoleBusy()["desk-pr"]; got != wantBusy {
					t.Errorf("RoleBusy()[desk-pr] = %v after a decline of kind %q with reason %q, want %v", got, kind, reason, wantBusy)
				}
			})
		}
	}
}

package orchestrator

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

type replyHandler struct {
	seqHandler
	reply wireclient.Reply
}

func (h *replyHandler) Dispatch(ctx context.Context, r roles.Role, evt eventqueue.Event) (wireclient.Reply, error) {
	if _, err := h.seqHandler.Dispatch(ctx, r, evt); err != nil {
		return wireclient.Reply{}, err
	}
	return h.reply, nil
}

type fakeSupervision struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeSupervision) OnSupervision(role, session string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, role+"/"+session+"/"+string(rune('0'+n)))
}

func TestOffer_reportsTheSupervisionOnlyWhenBothAreSet(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply wireclient.Reply
		want  int
	}{
		{"both", wireclient.Reply{ID: "d", Outcome: "o", Session: "s-1", Supervisors: 2}, 1},
		{"none", wireclient.Reply{ID: "d", Outcome: "o"}, 0},
		{"no session", wireclient.Reply{ID: "d", Outcome: "o", Supervisors: 2}, 0},
		{"no supervisors", wireclient.Reply{ID: "d", Outcome: "o", Session: "s-1"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrch(fastCfg(t), testQuerySet(nil, nil))
			o.Handler = &replyHandler{reply: tc.reply}
			f := &fakeSupervision{}
			o.SupervisionObserver = f
			l := o.NewListener(context.Background(), roles.Role{Name: "worker", Binds: []string{"pr.changed"}})
			l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")})
			if len(f.calls) != tc.want {
				t.Fatalf("calls = %v, want %d", f.calls, tc.want)
			}
			if tc.want == 1 && f.calls[0] != "worker/s-1/2" {
				t.Fatalf("call = %q, want worker/s-1/2", f.calls[0])
			}
		})
	}
}

// The re-offered counter is visible in a scrape of the metrics registry at the point
// the run context is cancelled and the offer is handed back, before Offer returns and
// so before the shutdown path flushes and exits.
func TestOffer_shutdownHandBackIsVisibleInTheRegistry(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	em, err := metrics.New(context.Background(), mp, func() map[string]int { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = &seqHandler{errs: []error{killedErr}}
	o.ReofferObserver = em
	l := o.NewListener(ctx, roles.Role{Name: "desk-pr", Binds: []string{"pr.changed"}})
	cancel()
	got := l.Offer(eventqueue.Offering{ID: "dsp", Event: retryEvent("e1")})
	if got.DeclineDetail != DeclineDetailShutdown {
		t.Fatalf("Offer() = %+v, want the shutdown hand-back", got)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var total int64 = -1
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metrics.MetricReofferedAfterShutdown {
				continue
			}
			s := m.Data.(metricdata.Sum[int64])
			for _, dp := range s.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key("role")); ok && v.AsString() == "desk-pr" {
					total = dp.Value
				}
			}
		}
	}
	if total != 1 {
		t.Fatalf("reoffered{role=desk-pr} = %d after the hand-back, want 1", total)
	}
}

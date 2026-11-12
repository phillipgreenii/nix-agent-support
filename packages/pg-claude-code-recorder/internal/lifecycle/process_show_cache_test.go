package lifecycle

import (
	"context"
	"reflect"
	"testing"
)

// process.show took 14 to 26 s on the live store because Process loaded the
// complete model, which every history write invalidates. It reads the
// registry-only model: a history entry or a stage row costs it nothing, a
// registry record it just ingested is found at once, and the answer equals the
// one derived from the complete model.
func TestProcessShowReadsNoHistoryAndFindsAFreshRegistration(t *testing.T) {
	v := newLiveEnv(t)
	ctx := context.Background()

	show := func(step string, wantReads bool) Process {
		t.Helper()
		v.cnt.Reset()
		got, err := v.e.Process(ctx, 73291, liveProcStart)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if reads := v.cnt.Queries(); wantReads != (reads > 0) {
			t.Fatalf("%s: %d queries, want reads=%v", step, reads, wantReads)
		}
		m, err := v.e.loadWith(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		if want := v.e.process(m, pkey{73291, liveProcStart}, true); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: show %+v, complete-model derivation %+v", step, got, want)
		}
		return got
	}

	if _, err := v.e.Process(ctx, 73291, liveProcStart); err != ErrNotFound {
		t.Fatalf("before registration: %v, want ErrNotFound", err)
	}
	v.k.AddRegistry("73291", liveRegistry(73291, liveSessA, "idle"))
	v.k.Parse()
	if p := show("fresh registration", true); !p.Alive || p.SessionID != liveSessA {
		t.Fatalf("fresh registration: %+v", p)
	}
	show("poll", false)

	// A history write moves the complete model, not this one.
	v.k.Add("", "history.jsonl", modelHistoryClear)
	v.k.Parse()
	show("history", false)

	v.k.AddRegistry("73291", liveRegistry(73291, liveSessA, "busy"))
	v.k.Parse()
	if p := show("registry update", true); p.Status != "busy" {
		t.Fatalf("registry update: %+v", p)
	}
}

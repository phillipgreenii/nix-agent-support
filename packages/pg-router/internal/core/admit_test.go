package core

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/levels"
)

// serveVerb runs one verb IN PROCESS through the participant boundary. Like
// serveStatus it may call t.Fatalf, so it MUST only be called from the test's
// own goroutine.
func serveVerb(t *testing.T, svc *Service, subcommand, request string) (map[string]any, int) {
	t.Helper()
	var out strings.Builder
	code := svc.Serve(subcommand, strings.NewReader(request), &out)
	var reply map[string]any
	if err := json.Unmarshal([]byte(out.String()), &reply); err != nil {
		t.Fatalf("reply %q is not JSON: %v", out.String(), err)
	}
	return reply, code
}

// admitLevels is a mutable level source for the queue under test.
type admitLevels struct {
	mu  sync.Mutex
	cur map[string]string
}

func (a *admitLevels) set(resource, level string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cur == nil {
		a.cur = map[string]string{}
	}
	a.cur[resource] = level
}

func (a *admitLevels) snapshot() levels.Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := levels.Snapshot{Now: time.Now(), Levels: map[string]levels.Level{}}
	for r, v := range a.cur {
		s.Levels[r] = levels.Level{Value: v, State: levels.StateHeld, Source: "sensor", Owner: "s"}
	}
	return s
}

func admitRegistry() levels.Registry {
	return levels.Registry{"disk": {Levels: []string{"PLENTY", "OK", "LOW"}, Unknown: "PLENTY"}}
}

func diskOK() levels.Requirement {
	return levels.Requirement{Clauses: []levels.Clause{{Text: "disk >= OK", Resource: "disk", Allowed: []string{"PLENTY", "OK"}}}}
}

func levelService(t *testing.T) (*Service, *admitLevels) {
	t.Helper()
	src := &admitLevels{}
	q, err := eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithLevelSource(src.snapshot), eventqueue.WithRegistry(admitRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	svc := startedServiceForStatus(t, nil)
	svc.q = q
	return svc, src
}

// AdmitPull forwards to the queue: a participant whose registered requirement
// is unmet is rejected naming the failing resource; one without a requirement
// is admitted at every level.
func TestAdmitPull_HonorsRegistrationTimeRequirements(t *testing.T) {
	svc, src := levelService(t)
	if _, err := svc.Register("plain", KindHandler); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register("watchdog", KindHandler); err != nil {
		t.Fatal(err)
	}
	svc.q.RegisterListener("plain", diskOK())
	if err := svc.AdmitPull("plain"); err != nil {
		t.Fatalf("level unknown reads best: AdmitPull = %v", err)
	}
	src.set("disk", "LOW")
	err := svc.AdmitPull("plain")
	var lb *eventqueue.LevelBlockedError
	if !errors.As(err, &lb) || len(lb.Result.Failing) != 1 || lb.Result.Failing[0].Resource != "disk" {
		t.Fatalf("AdmitPull(plain) = %v, want it rejected naming disk", err)
	}
	if !errors.Is(err, eventqueue.ErrLevelBlocked) {
		t.Fatal("the rejection must match ErrLevelBlocked")
	}
	if err := svc.AdmitPull("watchdog"); err != nil {
		t.Fatalf("AdmitPull(watchdog) = %v, want nil: it registered no requirement", err)
	}
	src.set("disk", "OK")
	if err := svc.AdmitPull("plain"); err != nil {
		t.Fatalf("after recovery AdmitPull = %v", err)
	}
}

// GATE-1: the register message lost nonBlockingGates; the schema rejects it.
func TestRegister_RejectsTheRemovedNonBlockingGatesField(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	_, code := serveVerb(t, svc, SubcommandRegister,
		`{"schemaVersion":"1","id":"x","kind":"handler","nonBlockingGates":["LOW_DISK_USAGE"]}`)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want a schema rejection", code)
	}
	reply, code := serveVerb(t, svc, SubcommandRegister, `{"schemaVersion":"1","id":"y","kind":"handler"}`)
	if code != conformance.ExitOK {
		t.Fatalf("a plain register exit = %d reply=%v", code, reply)
	}
}

// GATE-1 and Option B: the gate and pause / resume socket verbs are gone.
func TestRemovedGateAndPauseVerbsAreUnknown(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	for _, sub := range []string{"gate-set", "gate-clear", "pause", "resume"} {
		reply, code := serveVerb(t, svc, sub, `{"schemaVersion":"1"}`)
		if code != conformance.ExitError || reply["error"] == nil {
			t.Errorf("%s: exit=%d reply=%v, want the protocol error envelope", sub, code, reply)
		}
	}
}

// Level changes, pulls, status reads and the expiry sweep together under -race.
func TestAdmitPull_ConcurrentLevelChurn(t *testing.T) {
	svc, src := levelService(t)
	svc.q.RegisterListener("L", diskOK())
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			src.set("disk", []string{"PLENTY", "LOW", "OK"}[i%3])
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = svc.AdmitPull("L")
			var out strings.Builder
			svc.Serve(SubcommandStatus, strings.NewReader(`{"schemaVersion":"1"}`), &out)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			svc.q.Expire()
		}
	}()
	wg.Wait()
	reply, code := serveStatus(t, svc, statusRequest)
	if code != conformance.ExitOK {
		t.Fatalf("final status exit = %d", code)
	}
	if err := conformance.Check(StatusReplySchema, reply); err != nil {
		t.Fatalf("final status reply failed its schema: %v", err)
	}
	if lv, ok := reply["levels"].([]any); !ok || len(lv) != 0 {
		t.Fatalf("status levels = %v, want the empty array with no level projection wired", reply["levels"])
	}
}

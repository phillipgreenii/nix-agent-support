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
)

// --- Gate Registry socket API (bead pg2-h63eu) ------------------------------

// serveGateVerb runs one gate verb IN PROCESS through the participant
// boundary. Like serveStatus it may call t.Fatalf, so it MUST only be called
// from the test's own goroutine.
func serveGateVerb(t *testing.T, svc *Service, subcommand, request string) (map[string]any, int) {
	t.Helper()
	var out strings.Builder
	code := svc.Serve(subcommand, strings.NewReader(request), &out)
	var reply map[string]any
	if err := json.Unmarshal([]byte(out.String()), &reply); err != nil {
		t.Fatalf("reply %q is not JSON: %v", out.String(), err)
	}
	return reply, code
}

func activeTypes(svc *Service) []string {
	var out []string
	for _, g := range svc.ActiveGates() {
		out = append(out, g.Type)
	}
	return out
}

func TestGateSet_SetsRecordsAndRenews(t *testing.T) {
	svc := startedServiceForStatus(t, nil)

	reply, code := serveGateVerb(t, svc, SubcommandGateSet,
		`{"schemaVersion":"1","type":"LOW_DISK_USAGE","description":"3GiB free","owner":"disk-watchdog","ttlMs":330000}`)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d, want 0; reply=%v", code, reply)
	}
	if err := conformance.Check(GateSetReplySchema, reply); err != nil {
		t.Fatalf("reply failed cli.gate-set-reply: %v", err)
	}
	if reply["type"] != "LOW_DISK_USAGE" || reply["set"] != true || reply["renewal"] != false {
		t.Fatalf("reply = %v", reply)
	}
	if reply["expiresAt"] == nil {
		t.Fatalf("a TTL request must produce a lease: %v", reply)
	}
	g, ok := svc.q.Gate("LOW_DISK_USAGE")
	if !ok || g.Description != "3GiB free" || g.Owner != "disk-watchdog" {
		t.Fatalf("gate = %+v, ok=%v", g, ok)
	}

	// Setting it again is a RENEWAL — another GateSet (last writer wins): the
	// description, owner and lease are all overwritten.
	reply2, _ := serveGateVerb(t, svc, SubcommandGateSet,
		`{"schemaVersion":"1","type":"LOW_DISK_USAGE","description":"2GiB free","owner":"someone-else"}`)
	if reply2["renewal"] != true {
		t.Fatalf("second set: renewal = %v, want true", reply2["renewal"])
	}
	g, _ = svc.q.Gate("LOW_DISK_USAGE")
	if g.Description != "2GiB free" || g.Owner != "someone-else" || !g.ExpiresAt.IsZero() {
		t.Fatalf("after overwrite gate = %+v, want the last writer's fields and no lease", g)
	}
}

func TestGateSet_RejectsBadRequests(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	for name, req := range map[string]string{
		"lower-case type":   `{"schemaVersion":"1","type":"low_disk"}`,
		"missing type":      `{"schemaVersion":"1"}`,
		"zero ttl":          `{"schemaVersion":"1","type":"X","ttlMs":0}`,
		"unknown field":     `{"schemaVersion":"1","type":"X","severity":"high"}`,
		"wrong schema ver.": `{"schemaVersion":"9","type":"X"}`,
	} {
		reply, code := serveGateVerb(t, svc, SubcommandGateSet, req)
		if code != conformance.ExitError || reply["error"] == nil {
			t.Errorf("%s: exit=%d reply=%v, want the protocol error envelope", name, code, reply)
		}
	}
	if len(activeTypes(svc)) != 0 {
		t.Fatalf("a rejected request must not create a gate; active = %v", activeTypes(svc))
	}
}

func TestGateClear_AnyCallerAndIdempotent(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	if _, err := svc.SetGate(eventqueue.GateRequest{Type: "ALPHA", Owner: "alice"}); err != nil {
		t.Fatal(err)
	}

	reply, code := serveGateVerb(t, svc, SubcommandGateClear, `{"schemaVersion":"1","type":"ALPHA","by":"bob"}`)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d reply=%v", code, reply)
	}
	if err := conformance.Check(GateClearReplySchema, reply); err != nil {
		t.Fatalf("reply failed cli.gate-clear-reply: %v", err)
	}
	if got, _ := reply["cleared"].([]any); len(got) != 1 || got[0] != "ALPHA" {
		t.Fatalf("cleared = %v, want [ALPHA]: a different caller than the setter may clear it", reply["cleared"])
	}
	// Idempotent: clearing again succeeds and reports nothing cleared.
	reply, code = serveGateVerb(t, svc, SubcommandGateClear, `{"schemaVersion":"1","type":"ALPHA"}`)
	if code != conformance.ExitOK {
		t.Fatalf("second clear exit = %d", code)
	}
	if got, _ := reply["cleared"].([]any); got == nil || len(got) != 0 {
		t.Fatalf("second clear cleared = %v, want a present, empty array", reply["cleared"])
	}
}

func TestGateClear_AllAndSelectorValidation(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	for _, ty := range []string{"A", "B"} {
		if _, err := svc.SetGate(eventqueue.GateRequest{Type: ty}); err != nil {
			t.Fatal(err)
		}
	}
	for name, req := range map[string]string{
		"neither type nor all": `{"schemaVersion":"1"}`,
		"both type and all":    `{"schemaVersion":"1","type":"A","all":true}`,
	} {
		if _, code := serveGateVerb(t, svc, SubcommandGateClear, req); code != conformance.ExitError {
			t.Errorf("%s: exit = %d, want an error", name, code)
		}
	}
	if len(activeTypes(svc)) != 2 {
		t.Fatalf("rejected clears must change nothing; active = %v", activeTypes(svc))
	}
	reply, code := serveGateVerb(t, svc, SubcommandGateClear, `{"schemaVersion":"1","all":true}`)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if got, _ := reply["cleared"].([]any); len(got) != 2 {
		t.Fatalf("cleared = %v, want both", reply["cleared"])
	}
	if len(activeTypes(svc)) != 0 {
		t.Fatalf("active after clear --all = %v", activeTypes(svc))
	}
}

func TestPause_SetsSystemPauseWithOperatorAsOwner(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	reply, code := serveGateVerb(t, svc, SubcommandPause, `{"schemaVersion":"1","owner":"operator"}`)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d reply=%v", code, reply)
	}
	if err := conformance.Check(PauseReplySchema, reply); err != nil {
		t.Fatalf("reply failed cli.pause-reply: %v", err)
	}
	if reply["gate"] != GateSystemPause || reply["set"] != true {
		t.Fatalf("reply = %v, want SYSTEM_PAUSE set", reply)
	}
	g, ok := svc.q.Gate(GateSystemPause)
	if !ok || g.Owner != "operator" || g.Description == "" {
		t.Fatalf("SYSTEM_PAUSE = %+v ok=%v, want it set with the operator recorded as owner", g, ok)
	}
	// A re-pause is another GateSet (last writer wins), reported as a renewal.
	reply, _ = serveGateVerb(t, svc, SubcommandPause, `{"schemaVersion":"1","owner":"someone"}`)
	if reply["renewal"] != true {
		t.Fatalf("re-pause renewal = %v, want true", reply["renewal"])
	}
}

func TestResume_ClearsOnlySystemPauseUnlessAll(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	for _, ty := range []string{GateSystemPause, "LOW_DISK_USAGE"} {
		if _, err := svc.SetGate(eventqueue.GateRequest{Type: ty}); err != nil {
			t.Fatal(err)
		}
	}
	reply, code := serveGateVerb(t, svc, SubcommandResume, `{"schemaVersion":"1","by":"operator"}`)
	if code != conformance.ExitOK {
		t.Fatalf("exit = %d reply=%v", code, reply)
	}
	if err := conformance.Check(ResumeReplySchema, reply); err != nil {
		t.Fatalf("reply failed cli.resume-reply: %v", err)
	}
	if got := activeTypes(svc); len(got) != 1 || got[0] != "LOW_DISK_USAGE" {
		t.Fatalf("active after a bare resume = %v, want only the other system's gate left", got)
	}
	reply, _ = serveGateVerb(t, svc, SubcommandResume, `{"schemaVersion":"1","all":true}`)
	if got, _ := reply["cleared"].([]any); len(got) != 1 || got[0] != "LOW_DISK_USAGE" {
		t.Fatalf("resume --all cleared = %v", reply["cleared"])
	}
	// Resuming an already-resumed pool is a no-op success.
	reply, code = serveGateVerb(t, svc, SubcommandResume, `{"schemaVersion":"1"}`)
	if code != conformance.ExitOK {
		t.Fatalf("idempotent resume exit = %d", code)
	}
	if got, _ := reply["cleared"].([]any); got == nil || len(got) != 0 {
		t.Fatalf("idempotent resume cleared = %v, want an empty array", reply["cleared"])
	}
}

// The status reply lists ACTIVE gates only, with description/owner/lease, and
// a lapsed lease disappears without anyone clearing it.
func TestStatus_ReportsActiveGatesAndHonorsLeases(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	q, err := eventqueue.New(eventqueue.NewMemStore(), eventqueue.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	svc.q = q
	if _, err := svc.SetGate(eventqueue.GateRequest{Type: "LEASED", Description: "d", Owner: "o", TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetGate(eventqueue.GateRequest{Type: GateSystemPause}); err != nil {
		t.Fatal(err)
	}
	reply, _ := serveStatus(t, svc, statusRequest)
	if err := conformance.Check(StatusReplySchema, reply); err != nil {
		t.Fatalf("status reply failed its schema: %v", err)
	}
	gates, _ := reply["gates"].([]any)
	if len(gates) != 2 || gates[0].(map[string]any)["type"] != "LEASED" || gates[1].(map[string]any)["type"] != GateSystemPause {
		t.Fatalf("gates = %v, want both, sorted by TYPE", gates)
	}
	if leased := gates[0].(map[string]any); leased["ttlRemainingMs"] != float64(60000) || leased["owner"] != "o" {
		t.Fatalf("leased gate = %v", leased)
	}
	if _, has := gates[1].(map[string]any)["expiresAt"]; has {
		t.Fatalf("a lease-less gate must omit expiresAt: %v", gates[1])
	}

	now = now.Add(2 * time.Minute)
	reply, _ = serveStatus(t, svc, statusRequest)
	gates, _ = reply["gates"].([]any)
	if len(gates) != 1 || gates[0].(map[string]any)["type"] != GateSystemPause {
		t.Fatalf("after the lease lapsed gates = %v, want only SYSTEM_PAUSE", gates)
	}
}

// Participants declare non-blocking TYPEs at registration (the register verb's
// optional nonBlockingGates); AdmitPull rejects a blocked participant naming
// the gate and lets an exempt one through.
func TestAdmitPull_HonorsRegistrationTimeExemptions(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	reply, code := serveGateVerb(t, svc, SubcommandRegister,
		`{"schemaVersion":"1","id":"watchdog","kind":"handler","nonBlockingGates":["LOW_DISK_USAGE"]}`)
	if code != conformance.ExitOK {
		t.Fatalf("register exit = %d reply=%v", code, reply)
	}
	if _, err := svc.Register("plain", KindHandler); err != nil {
		t.Fatal(err)
	}
	if err := svc.AdmitPull("plain"); err != nil {
		t.Fatalf("no gate: AdmitPull = %v", err)
	}
	if _, err := svc.SetGate(eventqueue.GateRequest{Type: "LOW_DISK_USAGE"}); err != nil {
		t.Fatal(err)
	}
	err := svc.AdmitPull("plain")
	var gb *eventqueue.GateBlockedError
	if !errors.As(err, &gb) || gb.Gate != "LOW_DISK_USAGE" {
		t.Fatalf("AdmitPull(plain) = %v, want it rejected naming LOW_DISK_USAGE", err)
	}
	if err := svc.AdmitPull("watchdog"); err != nil {
		t.Fatalf("AdmitPull(watchdog) = %v, want nil: it declared LOW_DISK_USAGE non-blocking at registration", err)
	}
	if err := svc.AdmitPull("never-registered"); err == nil {
		t.Fatal("an unregistered participant blocks on every TYPE")
	}
	reg, _ := svc.reg.Get("watchdog")
	if len(reg.NonBlockingGates) != 1 || reg.NonBlockingGates[0] != "LOW_DISK_USAGE" {
		t.Fatalf("registration = %+v", reg)
	}
}

func TestRegister_RejectsNonCapsNonBlockingGate(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	_, code := serveGateVerb(t, svc, SubcommandRegister,
		`{"schemaVersion":"1","id":"x","kind":"handler","nonBlockingGates":["low_disk"]}`)
	if code != conformance.ExitError {
		t.Fatalf("exit = %d, want a schema rejection", code)
	}
}

// Concurrent set/clear/pause/resume/status churn: no data race (run with
// -race), no deadlock, and the final state is coherent.
func TestGateVerbs_ConcurrentChurn(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	var wg sync.WaitGroup
	verbs := []struct{ sub, req string }{
		{SubcommandPause, `{"schemaVersion":"1","owner":"a"}`},
		{SubcommandResume, `{"schemaVersion":"1"}`},
		{SubcommandGateSet, `{"schemaVersion":"1","type":"CHURN","ttlMs":1000}`},
		{SubcommandGateClear, `{"schemaVersion":"1","type":"CHURN"}`},
		{SubcommandStatus, `{"schemaVersion":"1"}`},
	}
	for _, v := range verbs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				var out strings.Builder
				svc.Serve(v.sub, strings.NewReader(v.req), &out)
			}
		}()
	}
	wg.Add(1)
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
}

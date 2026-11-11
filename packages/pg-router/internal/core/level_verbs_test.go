package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
)

type fakeRefresher struct {
	got []string
	out []LevelRefreshResult
}

func (f *fakeRefresher) RefreshLevels(_ context.Context, r []string) []LevelRefreshResult {
	f.got = r
	return f.out
}

func levelVerbService(t *testing.T) (*Service, *projFixture) {
	t.Helper()
	f := newProj(t)
	svc := startedServiceForStatus(t, nil)
	svc.levels = f.p
	return svc, f
}

func ingestJSON(f *projFixture, resource, level, owner string, off time.Duration) string {
	return `{"schemaVersion":"1","resource":"` + resource + `","level":"` + level + `","observed_at":"` +
		f.clk.now().Add(off).UTC().Format(time.RFC3339) + `","valid_for_s":180,"owner":"` + owner + `"}`
}

func entryFor(t *testing.T, reply map[string]any, resource string) map[string]any {
	t.Helper()
	for _, e := range reply["levels"].([]any) {
		if m := e.(map[string]any); m["resource"] == resource {
			return m
		}
	}
	t.Fatalf("no entry for %s in %v", resource, reply)
	return nil
}

func TestLevelIngest_outcomesAndReplySchema(t *testing.T) {
	svc, f := levelVerbService(t)
	body := ingestJSON(f, "cpu", "HIGH", "host-sensor", 0)
	for i, want := range []string{"accepted", "idempotent"} {
		reply, code := serveVerb(t, svc, SubcommandLevelIngest, body)
		if code != conformance.ExitOK || reply["outcome"] != want {
			t.Fatalf("call %d: code=%d reply=%v, want %s", i, code, reply, want)
		}
		if err := conformance.Check(LevelIngestReplySchema, reply); err != nil {
			t.Fatal(err)
		}
	}
	// the operator owner is reserved for the operator verbs
	reply, _ := serveVerb(t, svc, SubcommandLevelIngest, ingestJSON(f, "cpu", "LOW", "operator", 0))
	if reply["outcome"] != "rejected" || reply["reason"] != ReasonNotRegisteredSensor {
		t.Fatalf("operator owner: %v", reply)
	}
	reply, _ = serveVerb(t, svc, SubcommandLevelIngest, ingestJSON(f, "nope", "LOW", "host-sensor", 0))
	if reply["reason"] != ReasonUnknownResource {
		t.Fatalf("unknown resource: %v", reply)
	}
	reply, _ = serveVerb(t, svc, SubcommandLevelIngest, `{"schemaVersion":"1","resource":"cpu","level":"low"}`)
	if reply["outcome"] != "rejected" || reply["reason"] != ReasonMalformed {
		t.Fatalf("malformed: %v", reply)
	}
}

func TestLevelSet_overrideWinsThenExpiresToSensor(t *testing.T) {
	svc, f := levelVerbService(t)
	serveVerb(t, svc, SubcommandLevelIngest, ingestJSON(f, "cpu", "HIGH", "host-sensor", 0))
	reply, code := serveVerb(t, svc, SubcommandLevelSet, `{"schemaVersion":"1","resource":"cpu","level":"LOW","for_s":3600}`)
	if code != conformance.ExitOK || reply["until_cleared"] != false || reply["expires_at"] == nil {
		t.Fatalf("set: code=%d %v", code, reply)
	}
	if err := conformance.Check(LevelSetReplySchema, reply); err != nil {
		t.Fatal(err)
	}
	list, _ := serveVerb(t, svc, SubcommandLevelList, `{"schemaVersion":"1"}`)
	if err := conformance.Check(LevelListReplySchema, list); err != nil {
		t.Fatal(err)
	}
	e := entryFor(t, list, "cpu")
	ov, _ := e["override"].(map[string]any)
	if e["level"] != "LOW" || e["source"] != "operator" || ov == nil || ov["until_cleared"] != false || e["since"] == nil {
		t.Fatalf("override entry: %v", e)
	}
	f.clk.advance(3601 * time.Second)
	// the sensor record (180 s) is stale by now but still inside grace (2 x 180 s)? no: well past it
	list, _ = serveVerb(t, svc, SubcommandLevelList, `{"schemaVersion":"1"}`)
	e = entryFor(t, list, "cpu")
	if e["source"] == "operator" || e["override"] != nil {
		t.Fatalf("override should have expired: %v", e)
	}
	if len(f.expired) != 1 {
		t.Fatalf("expiry hook fired %d times, want once", len(f.expired))
	}
	// a fresh sensor answer is the value again
	serveVerb(t, svc, SubcommandLevelIngest, ingestJSON(f, "cpu", "HIGH", "host-sensor", 0))
	list, _ = serveVerb(t, svc, SubcommandLevelList, `{"schemaVersion":"1"}`)
	if e = entryFor(t, list, "cpu"); e["level"] != "HIGH" || e["source"] != "sensor" {
		t.Fatalf("after expiry: %v", e)
	}
}

func TestLevelSet_untilClearedNeverLapsAndClearRemovesIt(t *testing.T) {
	svc, f := levelVerbService(t)
	reply, code := serveVerb(t, svc, SubcommandLevelSet, `{"schemaVersion":"1","resource":"cpu","level":"HIGH","owner":"someone","until_cleared":true}`)
	if code != conformance.ExitOK || reply["until_cleared"] != true || reply["expires_at"] != nil {
		t.Fatalf("set: %d %v", code, reply)
	}
	f.clk.advance(30 * 24 * time.Hour)
	list, _ := serveVerb(t, svc, SubcommandLevelList, `{"schemaVersion":"1"}`)
	e := entryFor(t, list, "cpu")
	if e["level"] != "HIGH" || e["owner"] != "someone" || e["override"].(map[string]any)["until_cleared"] != true {
		t.Fatalf("until-cleared entry: %v", e)
	}
	reply, code = serveVerb(t, svc, SubcommandLevelClear, `{"schemaVersion":"1","resource":"cpu"}`)
	if code != conformance.ExitOK || len(reply["cleared"].([]any)) != 1 {
		t.Fatalf("clear: %d %v", code, reply)
	}
	if err := conformance.Check(LevelClearReplySchema, reply); err != nil {
		t.Fatal(err)
	}
	reply, _ = serveVerb(t, svc, SubcommandLevelClear, `{"schemaVersion":"1","resource":"cpu"}`)
	if len(reply["cleared"].([]any)) != 0 {
		t.Fatalf("second clear should be a no-op: %v", reply)
	}
	list, _ = serveVerb(t, svc, SubcommandLevelList, `{"schemaVersion":"1"}`)
	if e = entryFor(t, list, "cpu"); e["level"] != nil {
		t.Fatalf("cleared with no sensor answer should have no level: %v", e)
	}
}

func TestLevelSet_rejections(t *testing.T) {
	svc, _ := levelVerbService(t)
	for name, req := range map[string]string{
		"neither for_s nor until_cleared": `{"schemaVersion":"1","resource":"cpu","level":"LOW"}`,
		"both":                            `{"schemaVersion":"1","resource":"cpu","level":"LOW","for_s":60,"until_cleared":true}`,
		"over 24h":                        `{"schemaVersion":"1","resource":"cpu","level":"LOW","for_s":86401}`,
		"unknown resource":                `{"schemaVersion":"1","resource":"nope","level":"LOW","for_s":60}`,
		"level not in scale":              `{"schemaVersion":"1","resource":"cpu","level":"PLENTY","for_s":60}`,
	} {
		reply, code := serveVerb(t, svc, SubcommandLevelSet, req)
		if code != conformance.ExitError || reply["error"] == nil {
			t.Errorf("%s: code=%d reply=%v, want the error envelope", name, code, reply)
		}
	}
	// exactly 24h is accepted
	if _, code := serveVerb(t, svc, SubcommandLevelSet, `{"schemaVersion":"1","resource":"cpu","level":"LOW","for_s":86400}`); code != conformance.ExitOK {
		t.Errorf("24h override rejected")
	}
}

func TestLevelClear_allClearsEveryOverride(t *testing.T) {
	svc, _ := levelVerbService(t)
	serveVerb(t, svc, SubcommandLevelSet, `{"schemaVersion":"1","resource":"cpu","level":"LOW","until_cleared":true}`)
	serveVerb(t, svc, SubcommandLevelSet, `{"schemaVersion":"1","resource":"git_auth","level":"AVAILABLE","for_s":60}`)
	reply, code := serveVerb(t, svc, SubcommandLevelClear, `{"schemaVersion":"1","all":true}`)
	if code != conformance.ExitOK || len(reply["cleared"].([]any)) != 2 {
		t.Fatalf("clear all: %d %v", code, reply)
	}
	if _, code = serveVerb(t, svc, SubcommandLevelClear, `{"schemaVersion":"1"}`); code != conformance.ExitError {
		t.Fatalf("neither resource nor all must be refused")
	}
	if _, code = serveVerb(t, svc, SubcommandLevelClear, `{"schemaVersion":"1","resource":"nope"}`); code != conformance.ExitError {
		t.Fatalf("unknown resource must be refused")
	}
}

func TestLevelList_noReadingAndSince(t *testing.T) {
	svc, f := levelVerbService(t)
	list, _ := serveVerb(t, svc, SubcommandLevelList, `{"schemaVersion":"1"}`)
	if e := entryFor(t, list, "cpu"); e["level"] != nil || len(e) != 1 {
		t.Fatalf("a resource that never answered lists with the level absent: %v", e)
	}
	serveVerb(t, svc, SubcommandLevelIngest, ingestJSON(f, "cpu", "HIGH", "host-sensor", 0))
	first := f.clk.now()
	f.clk.advance(60 * time.Second)
	serveVerb(t, svc, SubcommandLevelIngest, ingestJSON(f, "cpu", "HIGH", "host-sensor", 0))
	list, _ = serveVerb(t, svc, SubcommandLevelList, `{"schemaVersion":"1"}`)
	if e := entryFor(t, list, "cpu"); e["since"] != first.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("since = %v, want the time the level was first entered %s", e["since"], first)
	}
	f.clk.advance(60 * time.Second)
	serveVerb(t, svc, SubcommandLevelIngest, ingestJSON(f, "cpu", "LOW", "host-sensor", 0))
	list, _ = serveVerb(t, svc, SubcommandLevelList, `{"schemaVersion":"1"}`)
	if e := entryFor(t, list, "cpu"); e["since"] != f.clk.now().UTC().Format(time.RFC3339Nano) {
		t.Fatalf("since after a change = %v", e["since"])
	}
}

func TestStatusReply_carriesLevelsAndValidates(t *testing.T) {
	svc, _ := levelVerbService(t)
	serveVerb(t, svc, SubcommandLevelSet, `{"schemaVersion":"1","resource":"cpu","level":"HIGH","for_s":60}`)
	reply, code := serveStatus(t, svc, statusRequest)
	if code != conformance.ExitOK {
		t.Fatalf("status exit %d", code)
	}
	if err := conformance.Check(StatusReplySchema, reply); err != nil {
		t.Fatalf("status reply: %v", err)
	}
	if e := entryFor(t, reply, "cpu"); e["source"] != "operator" {
		t.Fatalf("status entry: %v", e)
	}
	if _, has := reply["gates"]; has {
		t.Fatal("status still carries gates")
	}
}

func TestLevelRefresh_delegatesAndFormats(t *testing.T) {
	svc, _ := levelVerbService(t)
	if _, code := serveVerb(t, svc, SubcommandLevelRefresh, `{"schemaVersion":"1","resources":[]}`); code != conformance.ExitError {
		t.Fatal("no refresher must be an error")
	}
	fr := &fakeRefresher{out: []LevelRefreshResult{
		{Resource: "cpu", Outcome: "applied", Level: "HIGH"},
		{Resource: "git_auth", Outcome: "failed", Reason: "deadline"},
		{Resource: "x", Outcome: "no_source"},
	}}
	svc.levelRefresher = fr
	reply, code := serveVerb(t, svc, SubcommandLevelRefresh, `{"schemaVersion":"1","resources":["cpu","git_auth","x"]}`)
	if code != conformance.ExitOK || strings.Join(fr.got, ",") != "cpu,git_auth,x" {
		t.Fatalf("code=%d got=%v", code, fr.got)
	}
	if err := conformance.Check(LevelRefreshReplySchema, reply); err != nil {
		t.Fatal(err)
	}
	if n := len(reply["results"].([]any)); n != 3 {
		t.Fatalf("results = %d", n)
	}
}

func TestLevelVerbs_noProjectionIsAnError(t *testing.T) {
	svc := startedServiceForStatus(t, nil)
	for _, v := range []string{SubcommandLevelList, SubcommandLevelSet, SubcommandLevelClear, SubcommandLevelIngest} {
		if _, code := serveVerb(t, svc, v, `{"schemaVersion":"1"}`); code != conformance.ExitError {
			t.Errorf("%s: want an error without a projection", v)
		}
	}
}

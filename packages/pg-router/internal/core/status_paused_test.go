package core

import (
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/levels"
)

type idleListener struct{ id string }

func (l idleListener) ID() string                    { return l.id }
func (l idleListener) Matches(eventqueue.Event) bool { return false }
func (l idleListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	return eventqueue.OfferResult{}
}

// The status reply names every role a level requirement blocks, with the failing
// clause, the level, its source and owner, when the resource entered it, and the
// reading; a role no requirement blocks is absent; the reply still validates.
func TestStatusReply_pausedRolesCarryTheFailingClauses(t *testing.T) {
	svc, f := levelVerbService(t)
	q, err := eventqueue.New(eventqueue.NewMemStore(),
		eventqueue.WithLevelSource(f.p.Snapshot), eventqueue.WithRegistry(map[string]levels.Scale{
			"cpu": {Levels: []string{"LOW", "HIGH"}, Unknown: "LOW"},
		}))
	if err != nil {
		t.Fatal(err)
	}
	svc.q = q
	q.Register(idleListener{"worker"})
	q.Register(idleListener{"free"})
	q.RegisterListener("worker", levels.Requirement{Clauses: []levels.Clause{{Index: 0, Text: "cpu == LOW", Resource: "cpu", Allowed: []string{"LOW"}}}})

	reply, code := serveStatus(t, svc, statusRequest)
	if code != conformance.ExitOK {
		t.Fatalf("status exit %d", code)
	}
	if got := reply["pausedRoles"].([]any); len(got) != 0 {
		t.Fatalf("pausedRoles = %v with cpu unknown (best), want none", got)
	}

	serveVerb(t, svc, SubcommandLevelIngest, `{"schemaVersion":"1","resource":"cpu","level":"HIGH","observed_at":"`+
		f.clk.now().UTC().Format("2006-01-02T15:04:05Z")+`","valid_for_s":180,"owner":"host-sensor","reading":{"value":9.5,"unit":"load"}}`)
	reply, _ = serveStatus(t, svc, statusRequest)
	if err := conformance.Check(StatusReplySchema, reply); err != nil {
		t.Fatalf("status reply: %v", err)
	}
	paused := reply["pausedRoles"].([]any)
	if len(paused) != 1 {
		t.Fatalf("pausedRoles = %v, want exactly worker", paused)
	}
	row := paused[0].(map[string]any)
	fail := row["failing"].([]any)[0].(map[string]any)
	if row["role"] != "worker" || fail["clause"] != "cpu == LOW" || fail["level"] != "HIGH" || fail["owner"] != "host-sensor" || fail["since"] == nil {
		t.Fatalf("paused row = %v", row)
	}
	if r := fail["reading"].(map[string]any); r["value"] != 9.5 || r["unit"] != "load" {
		t.Fatalf("reading = %v", r)
	}
}

// A resource with no configured sensor lists no_sensor, and the reply validates.
func TestLevelEntries_noSensorFlag(t *testing.T) {
	svc, f := levelVerbService(t)
	f.p.cfg.Resources["cpu"] = LevelResource{Sensor: "host-sensor", GraceFactor: 2, NoSensor: true}
	list, _ := serveVerb(t, svc, SubcommandLevelList, `{"schemaVersion":"1"}`)
	if err := conformance.Check(LevelListReplySchema, list); err != nil {
		t.Fatal(err)
	}
	if e := entryFor(t, list, "cpu"); e["no_sensor"] != true {
		t.Fatalf("cpu entry = %v, want no_sensor", e)
	}
	if e := entryFor(t, list, "git_auth"); e["no_sensor"] != nil {
		t.Fatalf("git_auth entry = %v, want no no_sensor", e)
	}
}

package wireclient

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// Docket pg2-12gr4.10: a role that sets handler gets the role object
// {"name","config"} on every core-to-handler request body; a role without it
// gets a body byte-identical to the pre-split one (no role field at all).

const lifecycleOK = `{"schemaVersion":"1","id":"x","outcome":"ok"}`

// callers drives each of the four request builders once with a fakeRunner that
// answers every subcommand well-formed, and returns the request body it saw.
var roleObjectCallers = map[string]func(c *Client, role roles.Role) error{
	"dispatch": func(c *Client, role roles.Role) error {
		_, err := c.Dispatch(context.Background(), role, eventqueue.Event{ID: "e1", Type: "t", Payload: map[string]any{"k": "v"}})
		return err
	},
	"postStartup": func(c *Client, role roles.Role) error {
		_, err := c.PostStartup(context.Background(), role)
		return err
	},
	"preShutdown": func(c *Client, role roles.Role) error {
		_, err := c.PreShutdown(context.Background(), role)
		return err
	},
	"validate": func(c *Client, role roles.Role) error {
		_, err := c.Validate(context.Background(), role)
		return err
	},
}

func roleObjectRunner(name string) *fakeRunner {
	if name == "validate" {
		return &fakeRunner{stdout: []byte(`{"schemaVersion":"1","problems":[],"warnings":[]}`)}
	}
	return &fakeRunner{stdout: []byte(lifecycleOK)}
}

func TestRequests_carryRoleObjectForARoleWithHandler(t *testing.T) {
	cfg := map[string]any{"actor": "a", "nested": map[string]any{"n": float64(3)}}
	role := roles.Role{Name: "review", Handler: "/opt/h", Config: cfg}
	for name, call := range roleObjectCallers {
		t.Run(name, func(t *testing.T) {
			run := roleObjectRunner(name)
			c := &Client{Runner: run, Command: fixedCommand("/opt/h")}
			if err := call(c, role); err != nil {
				t.Fatal(err)
			}
			var req map[string]any
			if err := json.Unmarshal(run.gotStdin, &req); err != nil {
				t.Fatalf("body is not JSON: %v: %s", err, run.gotStdin)
			}
			want := map[string]any{"name": "review", "config": cfg}
			if !reflect.DeepEqual(req["role"], want) {
				t.Fatalf("role = %#v, want %#v (a top-level key beside schemaVersion and id)", req["role"], want)
			}
			if err := conformance.Check("handler."+name, req); err != nil {
				t.Fatalf("request with a role object fails its own schema: %v\n%s", err, run.gotStdin)
			}
		})
	}
}

// config is ALWAYS present on the wire, {} when the role has none.
func TestRequests_roleConfigIsEmptyObjectWhenAbsent(t *testing.T) {
	role := roles.Role{Name: "r", Handler: "/opt/h"} // Config nil
	for name, call := range roleObjectCallers {
		t.Run(name, func(t *testing.T) {
			run := roleObjectRunner(name)
			c := &Client{Runner: run, Command: fixedCommand("/opt/h")}
			if err := call(c, role); err != nil {
				t.Fatal(err)
			}
			var req struct {
				Role map[string]json.RawMessage `json:"role"`
			}
			if err := json.Unmarshal(run.gotStdin, &req); err != nil {
				t.Fatal(err)
			}
			if got := string(req.Role["config"]); got != "{}" {
				t.Fatalf("role.config = %q, want {} (never null, never absent): %s", got, run.gotStdin)
			}
		})
	}
}

func TestRequests_noRoleFieldForARoleWithoutHandler(t *testing.T) {
	role := roles.Role{Name: "old", Binds: []string{"t"}}
	for name, call := range roleObjectCallers {
		t.Run(name, func(t *testing.T) {
			run := roleObjectRunner(name)
			c := &Client{Runner: run, Command: fixedCommand("shared")}
			if err := call(c, role); err != nil {
				t.Fatal(err)
			}
			var req map[string]json.RawMessage
			if err := json.Unmarshal(run.gotStdin, &req); err != nil {
				t.Fatal(err)
			}
			if _, present := req["role"]; present {
				t.Fatalf("a role without handler must not carry a role field (the old handler would reject it): %s", run.gotStdin)
			}
		})
	}
}

// The body for a role without handler is byte-identical to what the builders
// produced before the role field existed: the lifecycle bodies are exactly the
// two-key envelope.
func TestLifecycleRequestBytesUnchangedForARoleWithoutHandler(t *testing.T) {
	for _, name := range []string{"postStartup", "preShutdown", "validate"} {
		t.Run(name, func(t *testing.T) {
			run := roleObjectRunner(name)
			c := &Client{Runner: run, Command: fixedCommand("shared")}
			if err := roleObjectCallers[name](c, roles.Role{Name: "old"}); err != nil {
				t.Fatal(err)
			}
			var req struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(run.gotStdin, &req); err != nil {
				t.Fatal(err)
			}
			want := `{"schemaVersion":"1","id":"` + req.ID + `"}`
			if string(run.gotStdin) != want {
				t.Fatalf("body = %s, want %s", run.gotStdin, want)
			}
		})
	}
}

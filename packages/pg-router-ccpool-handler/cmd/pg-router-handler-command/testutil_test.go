package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// testDeps keeps bd, ccpool and the event log off the machine: the command
// adapter reads only Cfg (argv template context) and the commander.
func testDeps(cfg config.Config, _ roles.Role) executor.Deps { return executor.Deps{Cfg: cfg} }

// request builds a wire request body of the given message type fields plus the
// role object: role nil omits it, config nil omits only the config.
func request(t *testing.T, extra map[string]any, roleName string, cfg map[string]any, withRole bool) string {
	t.Helper()
	body := map[string]any{"schemaVersion": "1", "id": "req-1"}
	for k, v := range extra {
		body[k] = v
	}
	if withRole {
		role := map[string]any{"name": roleName}
		if cfg != nil {
			role["config"] = cfg
		}
		body["role"] = role
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func eventWith(payload map[string]any) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	return map[string]any{"event": map[string]any{"id": "evt-1", "type": "scratch.event", "payload": payload}}
}

// cmdConfig is the [role.config] of a command role.
func cmdConfig(strict bool, argv ...string) map[string]any {
	a := make([]any, len(argv))
	for i, s := range argv {
		a[i] = s
	}
	cfg := map[string]any{"command": map[string]any{"argv": a}}
	if strict {
		cfg["strictBaseEnv"] = true
	}
	return cfg
}

// dispatch runs serveDispatch over req and returns the exit code and the reply.
func dispatch(ctx context.Context, t *testing.T, req string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	exit := serveDispatch(ctx, strings.NewReader(req), &out, "", testDeps)
	return exit, out.String()
}

func decodeReply(t *testing.T, reply string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(reply), &m); err != nil {
		t.Fatalf("reply %q is not a JSON object: %v", reply, err)
	}
	return m
}

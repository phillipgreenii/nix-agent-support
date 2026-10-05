package apply

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// deskBinary is the ambient $PATH name that is exec'd; this module never
// imports packages/pg-desk.
const deskBinary = "pg-desk"

// forceReviewKey is the one annotation key a decider may clear.
const forceReviewKey = "force_review"

// deciderName is the decider's name for an entity type (pr -> pr-decider).
func deciderName(typ string) string { return typ + "-decider" }

func origin(typ string) string { return "decider:" + deciderName(typ) }

// desk runs pg-desk and returns an error carrying its stderr on failure.
func desk(ctx context.Context, env Env, args ...string) error {
	cmd := env.command()(ctx, deskBinary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = "no diagnostic on stderr"
			}
			return fmt.Errorf("pg-desk %s: exit %d: %s", strings.Join(args, " "), exitErr.ExitCode(), detail)
		}
		return fmt.Errorf("exec pg-desk %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func actor(env Env) string { return env.Config.ActorOrDefault() }

// Annotate execs `pg-desk <typ> annotate <id> --key K --value V --origin
// decider:<name> --actor A`. For type pr the origin is decider:pr-decider.
func Annotate(ctx context.Context, env Env, typ, id, key, value string) error {
	return desk(ctx, env, typ, "annotate", id, "--key", key, "--value", value, "--origin", origin(typ), "--actor", actor(env))
}

// clearAnnotation removes an annotation. Only force_review on a pr is
// clearable, through the force-review clear path; anything else is an error.
func clearAnnotation(ctx context.Context, env Env, typ, id, key string) error {
	if typ != "pr" || key != forceReviewKey {
		return fmt.Errorf("annotation %q cannot be cleared on a %s: only %s on a pr is clearable", key, typ, forceReviewKey)
	}
	return desk(ctx, env, "pr", "force-review", id, "--clear", "--origin", origin(typ), "--actor", actor(env))
}

// refresh re-hydrates a work item in pg-desk after an external tracker write.
func refresh(ctx context.Context, env Env, workItemID string) error {
	return desk(ctx, env, "issue", "refresh", workItemID)
}

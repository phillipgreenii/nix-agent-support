package parity

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// legacyAsOf is the as-of time of a legacy row: before every fixture answer
// (fixtureAsOf), so a refresh visibly replaces it.
const legacyAsOf = "2026-09-01T00:00:00Z"

// sqlite3Bin returns the sqlite3 program the store helper drives: Env.Sqlite3Bin
// when set, else the first sqlite3 on the PATH of the process (a developer
// running the gate by hand). The nix check names it explicitly.
func (e Env) sqlite3Bin() (string, error) {
	if e.Sqlite3Bin != "" {
		if !isExecutable(e.Sqlite3Bin) {
			return "", fmt.Errorf("parity: Env.Sqlite3Bin %q is not an executable file", e.Sqlite3Bin)
		}
		return e.Sqlite3Bin, nil
	}
	p, err := exec.LookPath("sqlite3")
	if err != nil {
		return "", errors.New("parity: a scenario seeds a legacy row and needs sqlite3: set PG_DECIDER_PARITY_SQLITE3_BIN or put sqlite3 on PATH")
	}
	return p, nil
}

// seedLegacyRow is the store helper that replaces `pg-desk run pr` for the
// one scenario seeded the legacy way (design 6.7): it writes the entity row an
// old-schema store held, directly into the sandbox's version-1 store with the
// sqlite3 command line. The cutover that follows leaves the row exactly as the
// real cutover leaves every pre-existing row: version 0, never hydrated
// (hydrated_at NULL), active. The helper touches only the sandbox's own store
// (the caller has already verified it lies under the run directory) and runs
// BEFORE the cutover, so it never writes a version-2 table.
func (sb *sandbox) seedLegacyRow(ctx context.Context, sqlite3, entity string) error {
	if v, err := sb.schemaVersion(ctx, sqlite3); err != nil {
		return err
	} else if v != 1 {
		return fmt.Errorf("parity: the legacy row is written into a version-1 store, but the sandbox store is at version %d", v)
	}
	i := strings.LastIndex(entity, "#")
	if i <= 0 {
		return fmt.Errorf("parity: entity %q is not <repo>#<number>", entity)
	}
	repo := entity[:i]
	stmt := fmt.Sprintf(
		"INSERT INTO entity (repo, entity_type, entity_id, facts, as_of, stale, content_hash) VALUES (%s, 'pr', %s, '{}', %s, 0, 'legacy-never-hydrated');",
		sqlQuote(repo), sqlQuote(entity), sqlQuote(legacyAsOf),
	)
	return sb.sqlite(ctx, sqlite3, stmt)
}

// schemaVersion reads the store's PRAGMA user_version with the same sqlite3.
func (sb *sandbox) schemaVersion(ctx context.Context, sqlite3 string) (int, error) {
	out, err := exec.CommandContext(ctx, sqlite3, sb.storePath(), "PRAGMA user_version;").Output()
	if err != nil {
		return 0, fmt.Errorf("parity: read schema version of %s: %w", sb.storePath(), err)
	}
	var v int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &v); err != nil {
		return 0, fmt.Errorf("parity: schema version %q of %s: %w", strings.TrimSpace(string(out)), sb.storePath(), err)
	}
	return v, nil
}

func (sb *sandbox) sqlite(ctx context.Context, sqlite3, stmt string) error {
	if out, err := exec.CommandContext(ctx, sqlite3, sb.storePath(), stmt).CombinedOutput(); err != nil {
		return fmt.Errorf("parity: sqlite3 %s: %w: %s", sb.storePath(), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// readEntityRow returns version and hydrated_at of one entity row of the
// sandbox store, so a test can prove the legacy row is what it claims to be.
func (sb *sandbox) readEntityRow(ctx context.Context, sqlite3, entity string) (version int, hydratedAt string, err error) {
	out, err := exec.CommandContext(ctx, sqlite3, "-separator", "|", sb.storePath(),
		"SELECT version, COALESCE(hydrated_at, '<null>') FROM entity WHERE entity_id = "+sqlQuote(entity)+";").Output()
	if err != nil {
		return 0, "", fmt.Errorf("parity: read entity row of %s: %w", entity, err)
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d|%s", &version, &hydratedAt); err != nil {
		return 0, "", fmt.Errorf("parity: entity row of %s %q: %w", entity, strings.TrimSpace(string(out)), err)
	}
	return version, hydratedAt, nil
}

// sqlQuote quotes s as one SQL string literal.
func sqlQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

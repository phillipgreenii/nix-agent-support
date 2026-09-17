package main

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// importPgPrEntityType is the pg-desk entity_type recorded for every
// PR-level annotation row this command writes; matches entityTypePR
// (desk.go), the one entity type every other command in this package
// operates on.
const importPgPrEntityType = "pr"

// importPgPrSetBy is the set_by value recorded for every annotation row
// this command writes, so a later reader can tell a migrated row from one
// an operator or agent set directly through hide/unhide/wip.
const importPgPrSetBy = "import-pg-pr-annotations"

// importPgPrSourceBusyTimeoutMillis mirrors internal/store's own
// busyTimeoutMillis. pg-pr's store may still be open (WAL mode, per its own
// internal/store/store.go) while this command runs at the docket's own
// checkpoint or during the Phase 10 soak (see
// docs/behavior/pg-desk/import-pg-pr-annotations.md), so a short busy
// timeout on this read-only connection avoids a spurious "database is
// locked" on transient contention with a concurrent pg-pr writer.
const importPgPrSourceBusyTimeoutMillis = 5000

// importPgPrAnnotationsFlags holds parsed CLI flags for
// `pg-desk import-pg-pr-annotations`.
type importPgPrAnnotationsFlags struct {
	// sourceStore is the path to the pg-pr store.db being migrated FROM —
	// the command's one documented flag, --store (see
	// docs/behavior/pg-desk/import-pg-pr-annotations.md).
	sourceStore string
}

var ipaF importPgPrAnnotationsFlags

// importPgPrDeskStoreOpen opens pg-desk's OWN destination store (the
// target of this migration). A package-level var, mirroring
// packages/pg-pr/cmd/pg-pr/migrate_cmd.go's migrateStoreOpen, so tests can
// inject a store backed by a temp file instead of the real
// $XDG_STATE_HOME/pg-desk/store.db.
var importPgPrDeskStoreOpen = func() (*store.Store, error) {
	return store.Open(store.DefaultPath())
}

// importPgPrNow is the clock; overridable in tests, mirroring
// packages/pg-pr/internal/store/pull_request.go's nowRFC3339 var.
var importPgPrNow = func() string { return time.Now().UTC().Format(time.RFC3339) }

// importPgPrAnnotationsCmd is the one-shot cutover tool copying pg-pr's
// pull_request.user_hidden/user_hidden_reason/wip columns into pg-desk's
// annotation table. See docs/behavior/pg-desk/import-pg-pr-annotations.md.
//
// Exit codes (per the behavior doc): 0 on success, including a no-op
// second run; 1 when --store cannot be read as a pg-pr store, or
// pg-desk's own store cannot be opened/written — both surface as a
// non-nil RunE error, which main() turns into exit 1.
var importPgPrAnnotationsCmd = &cobra.Command{
	Use:   "import-pg-pr-annotations",
	Short: "One-shot cutover: copy pg-pr's hidden/wip annotations into the pg-desk store",
	RunE: func(cmd *cobra.Command, args []string) error {
		if ipaF.sourceStore == "" {
			return fmt.Errorf("import-pg-pr-annotations: --store is required (path to pg-pr's store.db)")
		}

		desk, err := importPgPrDeskStoreOpen()
		if err != nil {
			return fmt.Errorf("import-pg-pr-annotations: open pg-desk store: %w", err)
		}
		defer func() { _ = desk.Close() }()

		copied, err := importPgPrAnnotations(ipaF.sourceStore, desk)
		if err != nil {
			return fmt.Errorf("import-pg-pr-annotations: %w", err)
		}

		_, err = fmt.Fprintf(cmd.OutOrStdout(), "import-pg-pr-annotations: ok — copied %d row(s)\n", copied)
		return err
	},
}

func init() {
	importPgPrAnnotationsCmd.Flags().StringVar(&ipaF.sourceStore, "store", "",
		"Path to the pg-pr store.db to migrate from (required)")
	rootCmd.AddCommand(importPgPrAnnotationsCmd)
}

// importPgPrAnnotations copies every pull_request row's user_hidden,
// user_hidden_reason, and wip column from the pg-pr store at sourcePath
// into desk's annotation table — one PR-level Annotation row
// (comment_id "") per pull_request row. No other pull_request column is
// read (this packet's Contract).
//
// EntityID is written in the same qualified "<repo>#<n>" form
// resolvePRRef (desk.go) reconstructs for every hide/unhide/wip/feedback
// write and show read, not a bare decimal number (pg2-tlwh2): before
// pg2-276sg's fix this bare form was accidentally consistent with those
// commands' own (then-buggy) bare-number writes, but afterward a bare-key
// row from this import tool would silently stop round-tripping with the
// qualified keys every other write path now uses.
//
// It is idempotent by construction: UpsertAnnotation is an
// INSERT ... ON CONFLICT DO UPDATE keyed by (repo, entity_type, entity_id,
// comment_id), so running this twice against an unchanged source
// overwrites each row with the same values rather than inserting a
// duplicate or toggling anything. Returns the number of pull_request rows
// copied.
//
// The source is opened read-only via the same modernc.org/sqlite driver
// pg-desk's own store already depends on (see internal/store/store.go) —
// deliberately not packages/pg-pr's own store package: pg-desk's go.mod
// declares no dependency on pg-pr, and pg-pr's internal/store package is
// not importable across the module boundary regardless (Go's internal/
// visibility rule).
func importPgPrAnnotations(sourcePath string, desk *store.Store) (int, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(%d)", sourcePath, importPgPrSourceBusyTimeoutMillis)
	src, err := sql.Open("sqlite", dsn)
	if err != nil {
		return 0, fmt.Errorf("open pg-pr store %s: %w", sourcePath, err)
	}
	defer func() { _ = src.Close() }()

	rows, err := src.Query(`SELECT repo, number, user_hidden, user_hidden_reason, wip FROM pull_request`)
	if err != nil {
		return 0, fmt.Errorf("read pg-pr store %s: %w", sourcePath, err)
	}
	defer func() { _ = rows.Close() }()

	setAt := importPgPrNow()
	copied := 0
	for rows.Next() {
		var repo, hiddenReason string
		var number, userHidden, wip int
		if err := rows.Scan(&repo, &number, &userHidden, &hiddenReason, &wip); err != nil {
			return copied, fmt.Errorf("scan pg-pr pull_request row: %w", err)
		}

		hidden := userHidden != 0
		wipFlag := wip != 0
		a := store.Annotation{
			Repo:         repo,
			EntityType:   importPgPrEntityType,
			EntityID:     repo + "#" + strconv.Itoa(number),
			Hidden:       &hidden,
			HiddenReason: hiddenReason,
			WIP:          &wipFlag,
			SetBy:        importPgPrSetBy,
			SetAt:        setAt,
		}
		if err := desk.UpsertAnnotation(a); err != nil {
			return copied, fmt.Errorf("upsert annotation for %s#%d: %w", repo, number, err)
		}
		copied++
	}
	if err := rows.Err(); err != nil {
		return copied, fmt.Errorf("read pg-pr store %s: %w", sourcePath, err)
	}
	return copied, nil
}

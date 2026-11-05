//go:build ablation

package store

// Ablation hooks for the opt-in insert-cost harness (internal/parser,
// ablation tag). They exist only in a build with the ablation tag; a normal
// build uses the constants of ablation_off.go.
var (
	// ablateJournal is the journal_mode of every connection (WAL by default).
	ablateJournal = "WAL"
	// ablateDSNExtra is appended to the DSN of every connection (for example
	// "&_pragma=temp_store(2)").
	ablateDSNExtra string
	// ablateNoSavepoint skips the per-write SAVEPOINT of a bulk write. A
	// failed write then cannot be undone alone; the harness corpus has none.
	ablateNoSavepoint bool
)

// SetAblation sets the hooks; call it before Open. An empty journal keeps WAL.
func SetAblation(journal, dsnExtra string, noSavepoint bool) {
	if journal == "" {
		journal = "WAL"
	}
	ablateJournal, ablateDSNExtra, ablateNoSavepoint = journal, dsnExtra, noSavepoint
}

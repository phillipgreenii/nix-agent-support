//go:build !ablation

package store

// Ablation hooks (see ablation_on.go): constants in a normal build, so the
// compiler removes every branch that reads them and behavior is unchanged.
const (
	ablateJournal     = "WAL"
	ablateDSNExtra    = ""
	ablateNoSavepoint = false
)

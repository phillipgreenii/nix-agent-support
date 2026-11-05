//go:build !ablation

package store

// Ablation hooks (see ablation_on.go): constants in a normal build, so the
// compiler removes every branch that reads them and behavior is unchanged.
const (
	ablateJournal     = "WAL"
	ablateDSNExtra    = ""
	ablateNoSavepoint = false
)

// ablateNoStepTimers turns the step clock reads off; false in a normal build.
const ablateNoStepTimers = false

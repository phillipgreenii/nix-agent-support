package runner

import "context"

// NoneAnalyst gathers nothing and files nothing. It is the backend of
// --analyst none and of --evidence-only: the run stops after the evidence
// stage and the finish stage deletes the staging directory.
type NoneAnalyst struct{}

// Name implements Analyst.
func (NoneAnalyst) Name() string { return "none" }

// Analyse implements Analyst.
func (NoneAnalyst) Analyse(context.Context, *State) error { return nil }

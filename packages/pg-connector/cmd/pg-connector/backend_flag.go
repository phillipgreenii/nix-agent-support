// backend_flag.go: the shared --backend flag registration every Tier-1
// verb in pr.go/issue.go/ci.go/scm.go carries (bead pg2-2j5ac.28.1, design
// 's "id-less op rule"). Unlike --output (output.go's addOutputFlag),
// which every verb inherits uniformly as a PERSISTENT flag on root,
// --backend is registered per-verb, LEAF-flag style: its exact effect
// (require it at N>1 for an id-less write, fan out unless it pins one for
// list, or merely validate it for a single-valued type like scm) differs
// by what KIND of op the verb is — only that verb's own file knows which
// meaning applies, so the mechanical registration lives here while each
// call site decides what to do with the resolved value.
package main

import "github.com/spf13/cobra"

const backendFlagName = "backend"

// addBackendFlag registers --backend on cmd and returns a pointer to its
// resolved value ("" when the flag was not passed). help is the verb's
// own description of what pinning does for IT specifically (fan-out vs.
// require vs. validate-only) — deliberately not one generic help string,
// since the effect genuinely differs per verb shape.
func addBackendFlag(cmd *cobra.Command, help string) *string {
	var backend string
	cmd.Flags().StringVar(&backend, backendFlagName, "", help)
	return &backend
}

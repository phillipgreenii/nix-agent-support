module github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report

go 1.26.0

require github.com/spf13/cobra v1.10.2

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
)

// pg-connector is a sibling module in this same repo, used by the test suite
// only (pkg/scriptout's fake-backend doubles). Production code never imports
// it: work-report execs the pg-connector binary from PATH and decodes its JSON
// output into its own small structs. The same "local replace => ../sibling"
// pattern packages/pg-desk's go.mod uses.
replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector => ../pg-connector

module github.com/phillipgreenii/pg-decider

go 1.26.10

require (
	github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk v0.0.0-00010101000000-000000000000
	github.com/phillipgreenii/x v0.0.0-20261118145935-46d6423c44db
	github.com/spf13/cobra v1.10.2
	pgregory.net/rapid v1.3.0
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.1.0 // indirect
)

// pg-desk is a sibling module in this repo; only its published contract
// package pkg/deskapi (standard library only) is imported.
replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk => ../pg-desk

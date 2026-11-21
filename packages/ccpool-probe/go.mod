module github.com/phillipgreenii/ccpool-probe

go 1.26.10

require (
	github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector v0.0.0
	github.com/phillipgreenii/x v0.0.0-20261118145935-46d6423c44db
	github.com/spf13/cobra v1.10.2
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.1.0 // indirect
)

// pg-connector is a sibling module in this same repo: its published client
// package carries every call and decode of the pg-connector CLI; resolved by a
// local replace (phillipg-nix-repo-base ADR 0008).
replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector => ../pg-connector

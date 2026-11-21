module github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/work-report

go 1.26.10

require (
	github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector v0.0.0
	github.com/phillipgreenii/x v0.0.0-20261118145935-46d6423c44db
	github.com/spf13/cobra v1.10.2
	golang.org/x/text v0.44.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.62.0
)

require (
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-sqlite v0.0.0
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/spf13/pflag v1.1.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	modernc.org/libc v1.82.2 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.13.0 // indirect
)

// pg-connector is a sibling module in this same repo; production code imports
// only its published client package (standard library and x only), and the test
// suite also drives pkg/scriptout's fake-backend doubles. The same "local
// replace => ../sibling" pattern packages/pg-desk's go.mod uses.
replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector => ../pg-connector

replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-sqlite => ../pg-sqlite

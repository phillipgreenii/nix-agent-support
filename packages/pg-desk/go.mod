module github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk

go 1.26.0

require (
	github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector v0.0.0
	github.com/spf13/cobra v1.10.2
	golang.org/x/sys v0.48.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.59.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// pg-connector is a sibling module in this same repo, used only by
// internal/gather's own test suite to reuse pkg/scriptout/conformance's
// real wire-envelope schema checker rather than hand-rolling a parallel
// one (docket pg2-2j5ac.32, packet 4's own Files instruction) — resolved
// natively via a local replace, the same "local replace => ../sibling"
// pattern packages/pg-router-ccpool-handler's go.mod already uses for its
// own sibling-module dependencies (phillipg-nix-repo-base ADR 0008).
replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector => ../pg-connector

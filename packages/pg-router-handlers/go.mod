module github.com/phillipgreenii/pg-router-handlers

go 1.26.0

require (
	github.com/phillipgreenii/ccpool v0.0.0
	github.com/phillipgreenii/pg-failsig v0.0.0
	github.com/phillipgreenii/pg-router v0.0.0
	github.com/phillipgreenii/x v0.0.0-20261027205228-76ab0ad829aa
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder v0.0.0
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.49.0 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.61.0 // indirect
)

replace (
	github.com/phillipgreenii/ccpool => ../ccpool
	github.com/phillipgreenii/claude-transcript => ../claude-transcript
	github.com/phillipgreenii/pg-failsig => ../pg-failsig
	github.com/phillipgreenii/pg-router => ../pg-router
)

replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder => ../pg-claude-code-recorder

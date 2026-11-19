module github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-assistants

go 1.26.10

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/phillipgreenii/x v0.0.0-20261117200038-33c4146bc31f
	go.uber.org/goleak v1.3.0
)

require (
	github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder v0.0.0
	golang.org/x/sync v0.24.0 // indirect
)

replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder => ../pg-claude-code-recorder

replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-sqlite => ../pg-sqlite

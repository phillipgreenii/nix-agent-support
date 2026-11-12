module github.com/phillipgreenii/pg-router-sensors

go 1.26.8

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/phillipgreenii/pg-router v0.0.0
)

require github.com/phillipgreenii/x v0.0.0-20261106202408-ba17082cdd30

replace github.com/phillipgreenii/pg-router => ../pg-router

replace github.com/phillipgreenii/claude-transcript => ../claude-transcript

replace github.com/phillipgreenii/ccpool => ../ccpool

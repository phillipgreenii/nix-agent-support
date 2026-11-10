module github.com/phillipgreenii/pg-origin-guard

go 1.26.0

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/phillipgreenii/pg-failsig v0.0.0
)

replace github.com/phillipgreenii/pg-failsig => ../pg-failsig

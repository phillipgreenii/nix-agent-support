# pg-go-gate

> Host-wide slot gate for agent `go test`, `go build` and `go install`: at most 3 run at once, each under `nice -n 10`.
> The wait for a slot is bounded and fails open. Any other command runs untouched.
> More information: <https://github.com/phillipgreenii/phillipgreenii-nix-agent-support>.

- Run the Go tests through the gate:

`pg-go-gate go test -p 4 {{./...}}`

- Build through the gate:

`pg-go-gate go build {{./...}}`

- Allow only 2 slots and wait at most 60 seconds before running anyway:

`pg-go-gate --slots 2 --wait 60 go test {{./...}}`

- Use a custom lock root:

`pg-go-gate --dir {{/tmp/my-slots}} go build {{./...}}`

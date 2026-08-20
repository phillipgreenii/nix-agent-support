# Wire — decision docs

Realization decisions about how a message crosses a boundary.

### `DEC-WIRE-1` — the default transport is a CLI invocation carrying JSON <!-- uuid: 6450eed7-228f-4a99-bfd9-6705a6c552ee -->

**Decided.** The default transport contract invokes a participant as `<command> <subcommand>`,
carrying JSON on stdin and stdout.

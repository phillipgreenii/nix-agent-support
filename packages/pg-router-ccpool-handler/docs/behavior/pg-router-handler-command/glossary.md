# Glossary — pg-router-handler-command

Terms pg-router's set already defines (**role**, **adapter**, **role object**, **wrap**, **base
environment**, **handler session**, **pre-accept decline**) are **inherited** with their meaning there
(`INV-20`) and are not redefined. This set adds only the terms below.

- **Payload** — the command a dispatch ends up running: the role's argv, rendered for one event.
- **Rendered argv** — the role's argv after every element has been put through the template engine
  with the event's item fields. An element with no template action renders to itself.
- **Marker stage** — the innermost layer of the wrap chain, added by the adapter and never by the
  role's wrap: it writes a per-dispatch **ready marker** just before it starts the payload. A
  failure with no marker is a failure of the environment the payload runs in, not of the payload.
- **Ready marker** — the file the marker stage writes, named by an environment variable the adapter
  sets for the dispatch. Its existence says the payload started.
- **Busy exit** — the payload exiting with the coarse code that means "busy" (`9`,
  `phillipgreenii-nix-agent-support · packages/pg-router/docs/decisions · DEC-WIRE-1`), which the
  adapter turns into a pre-accept busy decline.
- **Stderr tail** — the bounded, redacted, one-line tail of a failed payload's standard error that
  the adapter puts in a failure's error text.

# pg-connector issue list

> List issues matching a named query across the registered issue backends.
> Query names, expressions and parameter patterns live only in the backend's config; a request supplies values only.
> More information: `pg-connector issue list --help`.

- List the issues a named query matches (every issue backend, or one with `--backend`):

`pg-connector issue list --query {{name}}`

- Run a parameterized query for one pull request (repeat `--param`; each value MUST match the regex the config declares). Pin `--backend`: `--param` needs a backend that declares `vocabulary.query_params`:

`pg-connector issue list --backend {{backend}} --query {{work-items-for-pr}} --param repo={{owner/name}} --param pr_number={{123}}`

- Record which tool made the call in the backend's event log (an environment variable; there is no `--caller` flag; unset records `unknown`):

`PG_CONNECTOR_CALLER={{decider}} pg-connector issue list --query {{name}}`

- Note: a query that declares `params` is never watched, and the backend binary and its config MUST ship in the same apply (a new config with an old backend answers `query_not_recognized` for every query).

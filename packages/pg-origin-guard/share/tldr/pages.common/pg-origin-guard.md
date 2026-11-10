# pg-origin-guard

> Keeps unattended git authentication retries from opening browser login windows, and reports whether auth to a realm is available.
> A realm is one authentication domain (one certificate). Exit 75 means unavailable or refused; only exit 0 means go ahead.
> More information: `pg-origin-guard help`.

- Check whether auth to a realm is available now (local certificate and recorded state only, never the network):

`pg-origin-guard check --realm {{zr}}`

- Check a git remote URL or a repository path, with machine-readable output:

`pg-origin-guard check --remote {{git@github.com:org/repo.git}} --json`

`pg-origin-guard check --repo {{/path/to/repo}} --json`

- Show every realm's state, reason and last error:

`pg-origin-guard status`

- Stop guarding a realm (the wrapper then runs its command unchanged), and resume:

`pg-origin-guard ignore {{zr}}`

`pg-origin-guard unignore {{zr}}`

- Use as an ssh ProxyCommand wrapper (the home module renders this; a refusal exits 75 with `pg-origin-guard: refused (<reason>)`):

`ProxyCommand pg-origin-guard proxy --realm {{zr}} -- {{step ssh proxycommand %r %h %p --provisioner Dex}}`

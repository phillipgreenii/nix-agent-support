# pg-gwm — the command, the lease and the record

This doc owns what a requester types and gets back: how the repository is chosen, the output formats,
the record every command returns, the statuses, `lease`, `renew`, `release`, `info` and `list`, how
names work, and the error catalog. How a worktree is created, reused and reaped is in
[`worktree-lifecycle.md`](worktree-lifecycle.md); concurrency, crashes and the scheduled reaper are in
[`failure-modes.md`](failure-modes.md).

## Stories

- **`STORY-GWM-ASK`** <!-- uuid: 153a81c4-061d-4df6-a9f3-bc3004ac2f53 --> — As a requester, I want to
  ask for a worktree of the repository I am in and be told where it is, so I never pick a path or
  collide with another requester's directory. _(→ `JOURNEY-GWM-LEASE-AND-RELEASE`; `GOAL-GWM-ASK-NOT-PATH`,
  `INV-GWM-1`, `INV-GWM-3`, `INV-GWM-4`.)_
- **`STORY-GWM-COME-BACK`** <!-- uuid: d5200aca-8fee-4864-b3e3-c666321b8826 --> — As a requester that
  stopped and started again (a redispatched agent, a person the next morning), I want to get the same
  worktree back by the name I gave it, so my commits and files are where I left them. _(→
  `JOURNEY-GWM-COME-BACK`; `INV-GWM-5`, `INV-GWM-7`, `INV-GWM-10`.)_
- **`STORY-GWM-DONE`** <!-- uuid: 4459c260-fab0-469f-bf11-fd95117a1bfe --> — As a requester, I want to
  say I am done with a worktree, so the manager can clean it and reuse or remove it, and I want to be
  stopped, with the exact remedy, if I still have unsaved work in it. _(→
  `JOURNEY-GWM-LEASE-AND-RELEASE`; `INV-GWM-9`.)_
- **`STORY-GWM-READ`** <!-- uuid: 26a87da0-b049-44b0-bfc4-67f3372bae18 --> — As a person or a program,
  I want to look up any managed worktree by its path, id, lease id or name, list them all, and find
  preserved work, in text or JSON, so I can see who holds what. _(→ `JOURNEY-GWM-INFO`;
  `GOAL-GWM-GENERIC`, `INV-GWM-2`, `INV-GWM-12`, `INV-GWM-13`.)_
- **`STORY-GWM-FULL`** <!-- uuid: d2500c2e-e116-4f71-b881-6148e25611b3 --> — As a requester of a
  repository with a maximum, I want a clear refusal when every worktree is in use, or to wait a bounded
  time for one, and success as soon as one is released or expires. _(→ `JOURNEY-GWM-MAX-REACHED`;
  `GOAL-GWM-BOUNDED`, `INV-GWM-14`, `INV-GWM-19`.)_

## Journeys

### `JOURNEY-GWM-LEASE-AND-RELEASE` — ask for a worktree, work, release it <!-- uuid: c9a78ecb-9149-41a0-9e31-5a12750065a9 -->

**Primary actor:** `ACTOR-REQUESTER`. **Other actors:** `ACTOR-HOST`. _Requires:_ `INV-GWM-1`,
`INV-GWM-3`, `INV-GWM-4`, `INV-GWM-5`, `INV-GWM-6`, `INV-GWM-9`, `INV-GWM-15`, `INV-GWM-16`,
`INV-GWM-17`, `INV-GWM-34`. _Includes:_ none.

1. The requester runs `pg-gwm lease`, from inside the repository or with `--repo`, optionally with a
   name, an owner, metadata and a time to live, and with the branch arguments of `git worktree add`
   (`-b`, `-B`, `--detach`, a commit-ish) (`INV-GWM-3`, `INV-GWM-16`, `INV-GWM-17`).
2. The manager records the lease (from this moment the name is held), then picks or creates a worktree
   and puts it on the branch and commit the request implies (`INV-GWM-5`, `INV-GWM-15`, `INV-GWM-16`).
3. The requester gets the record: worktree id, path, owner, branch, status `leased`, lease id, expiry,
   name, metadata and the rest of `INV-GWM-4`. `--print=path` prints only the path, for
   `cd "$(pg-gwm lease --print=path)"` (`INV-GWM-1`, `INV-GWM-2`, `INV-GWM-4`).
4. The requester works in the path and, if its lease has an expiry, renews it before then
   (`INV-GWM-6`).
5. The requester commits or discards its changes and runs `pg-gwm release`. The worktree becomes
   `released`, the name is free, and the lease cannot be renewed (`INV-GWM-9`). Each step is recorded in
   the event log (`INV-GWM-34`).

Extensions:

- 5a. The worktree is dirty or holds commits no branch reaches. Release refuses with `dirty`, lists what
  is unsaved and the commands that resolve it, and changes nothing (`INV-GWM-9`). The requester
  commits or discards its own work and releases again.
- 5b. The requester is gone and someone else (the operator, a supervisor) wants its worktree back,
  unsaved work and all. They run `pg-gwm expire <lease>`; the next reap preserves the work and
  reclaims the worktree (`INV-GWM-6`, `INV-GWM-24`).
- 2a. The repository has a maximum and is at it. See `JOURNEY-GWM-MAX-REACHED`.

### `JOURNEY-GWM-COME-BACK` — get the same worktree back by name <!-- uuid: a49dbbd4-4c6a-474f-8779-29c6af70b0ba -->

**Primary actor:** `ACTOR-REQUESTER`. _Requires:_ `INV-GWM-5`, `INV-GWM-7`, `INV-GWM-8`, `INV-GWM-10`.
_Includes:_ none.

1. Yesterday the requester leased a worktree named `potato` with a one-day time to live, and stopped.
2. Today it runs `pg-gwm renew --name potato`. Exactly one worktree with a leased or expired lease is
   named `potato`, so the manager renews that lease (`INV-GWM-5`, `INV-GWM-7`).
3. The requester gets the record with the same path, branch and lease id, a new expiry and, if it
   passed metadata, the new metadata (`INV-GWM-7`, `INV-GWM-8`).

Extensions:

- 2a. Reap has already reclaimed the expired worktree, or is reclaiming it. Renew fails with
  `lease-lost`, naming the preserved branch if reap saved changes; the requester leases again
  (`INV-GWM-7`, `INV-GWM-24`).
- 2b. The lease was released. Renew fails with `lease-released`. A new `pg-gwm lease --name potato`
  succeeds (`INV-GWM-5`, `INV-GWM-9`).
- 1a. A dispatcher does not know whether its earlier lease still exists. It runs
  `pg-gwm lease --name potato --or-renew`, which renews a leased or expired `potato` and otherwise
  leases a new one, in one step, and reports which it did (`INV-GWM-7`).
- 1b. A second requester runs `pg-gwm lease --name potato` (without `--or-renew`) while yesterday's
  lease is leased or expired. It fails with `name-held`, whose message says to renew instead
  (`INV-GWM-5`).

### `JOURNEY-GWM-MAX-REACHED` — every allowed worktree is in use <!-- uuid: 92401827-74fc-423c-bc23-5f6c163ac7ef -->

**Primary actor:** `ACTOR-REQUESTER`. _Requires:_ `INV-GWM-14`, `INV-GWM-19`, `INV-GWM-21`. _Includes:_ none.

1. The repository has `max = 4` and four worktrees are leased, none expired.
2. A fifth `pg-gwm lease` fails with `max-reached` and creates nothing (`INV-GWM-14`, `INV-GWM-21`).

Extensions:

- 2a. One of the four was released, or its lease has expired. The lease reclaims that one worktree
  first, preserving unsaved work, and succeeds; it does not run a full reap (`INV-GWM-19`).
- 2b. The lease was given `--wait 5m`. It waits up to five minutes for a release or an expiry, then
  succeeds or fails with `max-reached` (`INV-GWM-14`).

### `JOURNEY-GWM-INFO` — look a worktree up <!-- uuid: 3e8493ac-2866-455e-9597-0cb45bb5677c -->

**Primary actor:** `ACTOR-REQUESTER`. _Requires:_ `INV-GWM-2`, `INV-GWM-12`, `INV-GWM-13`. _Includes:_ none.

1. The requester runs `pg-gwm info <key>` and gets the same record a lease would return
   (`INV-GWM-12`).
2. `pg-gwm list` shows every managed worktree of the repository with its status; `pg-gwm preserved
list` shows the branches the manager wrote to save work (`INV-GWM-13`).

Extensions:

- 1a. The path is a worktree the manager does not manage, or the main checkout. The output is the path,
  branch and head commit, with status `unmanaged`, and a warning (`INV-GWM-12`).
- 1b. Run in a terminal, both print text; piped or run by a program, both print JSON (`INV-GWM-2`).

## Statuses

| Status          | Meaning                                                                              | Holds its name? | A new lease may take it?                                                  | Example                                                    |
| --------------- | ------------------------------------------------------------------------------------ | --------------- | ------------------------------------------------------------------------- | ---------------------------------------------------------- |
| `preparing`     | A lease is recorded and its worktree is being created or switched for it             | yes             | no                                                                        | the first lease of a large repository, minutes             |
| `leased`        | A lease whose expiry has not passed, or that has none                                | yes             | no                                                                        | an agent session working on `worker/bd-123`                |
| `lease-expired` | The expiry passed and reclaiming has not begun; renewal is possible, not promised    | yes             | only by reclaiming it first (`INV-GWM-19`, `INV-GWM-21`)                  | a dispatcher died two hours ago and stopped renewing       |
| `released`      | The owner said it is done; not yet reclaimed                                         | no              | yes when it holds the requested branch, else with reuse on (`INV-GWM-20`) | a review session finished                                  |
| `available`     | A standby worktree: no lease, ready                                                  | no              | yes (`INV-GWM-20`)                                                        | one of `min = max = 4`, waiting; a fresh standby from reap |
| `creating`      | A standby worktree is being created by reap, with no lease                           | no              | no                                                                        | reap topping up to `min`                                   |
| `reclaiming`    | Reap or a lease is preserving, cleaning or removing it; any lease it had is over     | no              | no                                                                        | reap committing a dirty expired worktree to its branch     |
| `broken`        | An operation stopped part-way, or its directory is gone while git still registers it | no              | no; reap retries it, then removes it (`INV-GWM-30`)                       | someone deleted the directory; a checkout was killed       |

A removed worktree leaves `list`; its history is in the event log (`INV-GWM-34`). `info` on a path the
manager does not manage reports the pseudo-status `unmanaged`.

## Invariants

- **`INV-GWM-1`** <!-- uuid: 3f2f6d70-a376-43dd-95ac-74b2f8b7a3ff --> — `lease` MUST NOT take a path
  from the requester. The manager MUST choose the worktree's directory, under the repository's
  `worktree-root`, and MUST return it in the record.
- **`INV-GWM-2`** <!-- uuid: 4e88c285-f8e3-48a9-b4f5-8a1208893bdd --> — Every subcommand MUST print
  text when its standard output is a terminal and JSON otherwise. `--format=text`, `--format=json` and
  its alias `--json` MUST override the default. `lease`, `renew` and `info` MUST accept `--print=path`,
  which prints only the worktree path and a newline. JSON output MUST carry a `schema_version`, which
  MUST increase on any change that removes or retypes a field; adding a field MUST NOT. Warnings MUST
  go to standard error in every format. The text output MUST show, at least, the path, name, status,
  branch, owner and expiry.
- **`INV-GWM-3`** <!-- uuid: ea253307-d7f2-4aed-8402-866afcd5f427 --> — The repository MUST be taken
  from `--repo <path>` when given, otherwise from the current directory. Any path inside the main
  checkout or inside any of its worktrees MUST resolve to the same repository and the same managed
  state. A path inside no repository MUST fail with `usage`.
- **`INV-GWM-4`** <!-- uuid: 56d0d5f3-8721-4dff-ae83-468cec80d3b0 --> — `lease`, `renew` and `info` of
  a managed worktree MUST return the same record: `id` (prefixed `w-`), `worktree_path`, `repo`, `owner`,
  `branch`, `head`, `status`, `lease_id` (prefixed `l-`), `lease_ttl`, `lease_expiration`, `name`,
  `metadata`, `created_at` and `managed`. In JSON an absent value MUST be `null`, never an empty string;
  `metadata` MUST be an object (`{}` when empty); times MUST be RFC 3339 in UTC; durations MUST use the
  grammar `none` or a number with a unit of `s`, `m`, `h` or `d`. `list` MUST return one record per
  managed worktree.
- **`INV-GWM-5`** <!-- uuid: 8ae02784-208b-4369-9a40-53843e103709 --> — A name is optional. A name,
  once given to a lease, MUST NOT change for that lease's life, and MUST be held from the moment the
  lease is recorded. A name labels the worktree, as a path labels an unmanaged one: it is usually
  correlated with the branch but MUST NOT determine it, and one worktree MAY carry different branches
  under different leases. A name MUST be unique, compared exactly, among the repository's worktrees
  whose lease is `preparing`, `leased` or `lease-expired`; `lease --name X` MUST fail with `name-held`
  while such a worktree is named X. A name MUST be free again once its lease is released or its
  worktree begins reclaiming, so last week's reclaimed `potato` never blocks today's. A name MUST be
  one line of printable characters, at most 100 long, and MAY contain `/`. _(Operator ruling, Phillip,
  2026-11-14: "the "name" is closer to the worktree path of a non managed ... You could reuse a managed
  worktree with different branches, so the name and branch are not the same.")_
- **`INV-GWM-6`** <!-- uuid: f7d0d6d9-434a-4b63-b822-cbde04e655f0 --> — A lease MUST have no expiry
  unless the request or `default-ttl` gives one, matching a plain git worktree. A `--ttl` MUST be a
  positive duration (`0s` or a negative one is a `usage` error) or, on `lease` only, `none`.
  `renew --ttl <d>` MUST set the expiry to now plus d, including on a lease that had none; `renew` with
  no `--ttl` MUST set the expiry to now plus the lease's recorded `lease_ttl` (a lease with none stays
  without one). Whether `renew` may remove an expiry is `OQ-GWM-TTL-REMOVE`. `expire <lease id> |
--name N` MUST set a leased lease's expiry to now and MUST NOT otherwise change it: the lease becomes
  `lease-expired`, its holder MAY still renew it until reclaiming begins, and the next reap, or a lease
  needing a candidate, preserves its unsaved work and reclaims it. `expire` is for someone other than
  the holder (the operator, a supervisor) who knows the holder is gone; its output MUST name the owner.
  Expiry MUST be judged against the host's clock in UTC. _(Operator rulings, Phillip, 2026-11-14:
  "i prefer the expire ... it is more clear" and "renew --ttl 0s should be an error".)_
- **`INV-GWM-7`** <!-- uuid: d3545c27-cd54-4135-a54f-f2b8dfbf7eaf --> — `renew` MUST accept a lease id
  or a name. A name MUST match only the single worktree whose lease is `leased` or `lease-expired` with
  that name. Renewing a `lease-expired` lease MUST succeed while reclaiming has not begun, and MUST fail
  with `lease-lost` once it has, naming any preserved branch; to answer that after the name is free,
  the manager MUST keep a record of each reclaimed lease (its id, name and preserved branch) for
  `tombstone-retention`, and a renew by name that matches only such a record MUST fail with
  `lease-lost`. Renew MUST NOT change the name, the owner, the path or the branch. `lease --name N
--or-renew` MUST, as one step under the repository's lock, renew the lease named N if it is leased or
  expired and otherwise lease a new one, and MUST report in the record whether it renewed.
- **`INV-GWM-8`** <!-- uuid: 7b6f91a0-5a78-4184-845c-2e5842a613c9 --> — When `renew` is given metadata
  it MUST replace the lease's metadata entirely (keys not given are dropped). When it is given none, the
  metadata MUST stay as it was. `lease` MUST accept owner, name and metadata.
- **`INV-GWM-9`** <!-- uuid: 649d5dc5-ae04-4a09-a927-729714991db9 --> — `release` MUST refuse with
  `dirty`, and change nothing, a worktree that is dirty or whose head is a commit no branch or tag
  reaches, because releasing claims the requester is done with it. The refusal MUST list the changed
  files or unreachable commits and the commands that resolve them. A successful release MUST end the
  lease: the worktree becomes `released`, the name is free, and any later `renew` or `release` of that
  lease MUST fail with `lease-released`; a `release` of a reclaimed lease MUST fail with `lease-lost`.
  Release MUST accept a lease id or a name, and its output MUST name the owner of the lease it ended.
- **`INV-GWM-10`** <!-- uuid: 2ee2c811-e9a6-4866-9f3b-788bd90f1efd --> — A lease id MUST be unique per
  lease and MUST never be reused, even after its worktree is reused. Holding a lease id, or the name of
  a valid or expired lease, MUST be enough to renew or release it; the owner field MUST NOT be checked.
  A program that holds a lease id SHOULD renew and release by it rather than by name.
- **`INV-GWM-11`** <!-- uuid: 9f2d2b17-94b1-4b17-a467-1e63284ecbc3 --> — Whether a lease is in force
  MUST be decided by its expiry alone. The manager MUST NOT probe for processes working in a worktree.
  A worktree reclaimed while something still works in it means its requester failed to renew; it is not
  the manager's error. _(Operator ruling, Phillip, 2026-11-14, quoted in the README.)_
- **`INV-GWM-12`** <!-- uuid: d4eab94f-3a91-439f-a011-4e9ba3c43436 --> — `info <key>` MUST resolve the
  key in this order: an existing path (or a path inside one); a worktree id (`w-…`); a lease id
  (`l-…`); then the name of a lease that is `preparing`, `leased` or `lease-expired`. `--path`,
  `--id`, `--lease-id` and `--name` MUST force one kind. It MUST return the record of `INV-GWM-4` for a
  managed worktree in any status, `broken` included. For a path inside a git worktree the manager does
  not manage, or the main checkout, it MUST return that worktree's path, branch and head with
  `managed: false` and status `unmanaged`, warn, and exit 0. A key that matches nothing MUST fail with
  `not-found`.
- **`INV-GWM-13`** <!-- uuid: 5409ee02-3343-4441-bb1f-a785cd4a3a5e --> — `list` MUST show every managed
  worktree of the repository in every status, with the record of `INV-GWM-4`, sorted by creation time
  with the oldest lease first. `--include-unmanaged` MUST add unmanaged worktrees, marked so.
  `preserved list` MUST list the branches the manager wrote to save work (`INV-GWM-24`), with the
  lease name, lease id and time each came from. `preserved prune [--older-than <d>] [--dry-run]` MUST
  delete only such branches, only those older than d or whose commit another branch already contains,
  and only when no worktree holds them; it MUST NOT delete any other branch.
- **`INV-GWM-14`** <!-- uuid: 8f8bd66c-2912-4673-8c21-67872ddad961 --> — A refusal MUST exit with the
  code of the error catalog below and MUST change nothing (the state, the worktree registrations and the
  branches are as before). In text it MUST print one line naming the reason and what to do; in JSON it
  MUST print `{schema_version, error: {name, code, message, hint, details}}` on standard output, where
  `details` carries the facts named in the catalog. `lease --wait <d>` MUST wait up to d for a release
  or an expiry before refusing with `max-reached`. Exit code 1 MUST mean only an unexpected error.

## The command interface

- **`INTF-GWM-CLI`** <!-- uuid: 9ac6c4a1-7bd9-4965-ad27-c96664bde6cf --> — the `pg-gwm` command.
  Every subcommand takes `--repo <path>`, `--format=text|json` and `--json`. What crosses in:
  - `lease [--name N [--or-renew]] [--owner O] [--metadata k=v]... [--ttl <duration>|none]
[-b B | -B B | --detach] [<commit-ish>] [--wait <duration>] [--print=path]`
  - `renew <lease id> | --name N [--ttl <duration>] [--metadata k=v]... [--print=path]`
  - `release <lease id> | --name N`
  - `expire <lease id> | --name N`
  - `info <key> | --path P | --id W | --lease-id L | --name N [--print=path]`
  - `list [--include-unmanaged]`
  - `preserved list`, `preserved prune [--older-than <duration>] [--dry-run]`
  - `reap [--all-repos] [--dry-run]`
  - `config check`, `completion <shell>`

  What crosses out: the record of `INV-GWM-4` (a JSON object, or a list of them for `list` and
  `reap`), or a refusal from the catalog below. What MUST hold: `INV-GWM-1` to `INV-GWM-14`. The
  counterparty is `ACTOR-REQUESTER`.

### Error catalog

The shared codes come from the workspace exit-code library: 1 internal, 2 usage, 4 not found, 9 busy.
The application's own codes start at 20.

| Code | Name               | When                                                                                                                                                                                                                         | `details`                        |
| ---- | ------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------- |
| 1    | `internal`         | anything unexpected                                                                                                                                                                                                          |                                  |
| 2    | `usage`            | bad flags; a path in no repository; `-b` naming an existing branch; a name that is not a valid branch name where it is used as one; a new branch under `pg-gwm/`; a `--ttl` that is not positive; an invalid template output | the offending flag or value      |
| 4    | `not-found`        | `info`, `renew`, `release` or `expire` names nothing                                                                                                                                                                         | the key                          |
| 9    | `busy`             | the repository's lock was not taken within `lock-wait` (`INV-GWM-29`)                                                                                                                                                        | the holder's command, if known   |
| 20   | `max-reached`      | at `max` with nothing released or expired to reclaim, after any `--wait`                                                                                                                                                     | `max`, count by status           |
| 21   | `name-held`        | `lease --name X` while a preparing, leased or expired X exists; the hint says to renew or use `--or-renew`                                                                                                                   | the holder's lease id and status |
| 22   | `branch-in-use`    | the branch is checked out in another worktree, managed or not                                                                                                                                                                | that worktree's path, name       |
| 23   | `dirty`            | `release` of a dirty worktree or one with unreachable commits                                                                                                                                                                | the files and commits            |
| 24   | `lease-released`   | `renew` or `release` of a released lease                                                                                                                                                                                     | the lease id                     |
| 25   | `lease-lost`       | `renew` or `release` of a lease whose worktree is reclaiming or reclaimed                                                                                                                                                    | the preserved branch, if any     |
| 26   | `ref-unresolvable` | the commit-ish does not resolve against local refs                                                                                                                                                                           | the reference, the git message   |
| 28   | `broken`           | `lease`, `renew` or `release` aimed at a `broken` worktree (`info` succeeds)                                                                                                                                                 | the worktree id                  |
| 29   | `low-disk`         | creating a worktree would cross `free-space-floor`                                                                                                                                                                           | free bytes, floor                |

Code 27 is left unused (an unconfigured repository works with defaults).

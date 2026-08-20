# Actors — pg-pr

Who interacts with pg-pr. Everything pg-pr integrates with is an actor — human or system; an
**interface** is _how_ an actor interacts. A behavior docs set MUST define all of its actors.

## Principals (human or agent)

- **`ACTOR-PGPR-OP` — Operator** <!-- uuid: cde34251-5b5a-44b5-8b7f-4587d373007a --> — a
  **principal — a human or an agent** — that reads PR facts, stages and posts reviews and
  comments, and creates or updates PRs through pg-pr's CLI (`INTF-PGPR-READ`,
  `INTF-PGPR-WRITE`).
- **`ACTOR-PGPR-CONSUMER` — Machine read consumer** <!-- uuid:
  ba9c4fb1-7614-498b-bd41-24bf5dd9a4ae --> — a program that reads pg-pr's PR-fact surface to
  drive its own workflow decisions (e.g. deciding whether a PR is ready to act on). pg-pr does
  not know or care who its consumers are; it serves the read seam to any caller
  (`INTF-PGPR-READ`).

## System actors

- **`ACTOR-PGPR-CODEHOST` — Code host** <!-- uuid: 5285b2f2-9303-4bb0-bd1c-b64f41969f11 --> —
  the external system of record for PRs, reviews, and comments. pg-pr is an **implementer** of
  the code host's own review-posting and query contracts: it cites the code host's shape and
  states only its own obligations, never restates the code host's contract
  (`INTF-PGPR-SYNC`, `INTF-PGPR-WRITE`).
- **`ACTOR-PGPR-TRACKER` — Work tracker** <!-- uuid: 55b3b02b-6096-49ae-b5de-2394302ecc9b --> —
  the external system holding merge-request tracking records. pg-pr is the **sole creator** of
  these records; the tracker itself owns their lifecycle beyond creation (`INTF-PGPR-MR`).

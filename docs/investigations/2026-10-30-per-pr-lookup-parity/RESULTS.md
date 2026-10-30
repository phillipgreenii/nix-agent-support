# Per-PR lookup parity spike (bead pg2-eclw3j.1)

Run 2026-10-30 against the zr tracker (`bd -C /Volumes/gitrepos/ziprecruiter/pristine`), read-only
(`bd list` only). Script: `parity.py` (re-runnable; bead pg2-eclw3j.8 re-runs it after the backfill).
Raw outputs: `out/summary.json`, `out/summary.md`, `out/per-pr.json`, `out/differences.tsv`,
`out/e3-bound.json`.

```sh
python3 -I docs/investigations/2026-10-30-per-pr-lookup-parity/parity.py --out <dir> [--all-prs] [--workers N]
```

`bd` answers are cached under `<dir>/cache` (git-ignored) so a failed run resumes; delete it for a
fresh read. The tracker was under load (bd i/o timeouts), so the script retries each call with a
back-off (0, 5, 20, 45 s) and defaults to 2 workers.

## Method

(a) Decider today: union by id of the six `work-items-all` expressions (rows 1606, 1618, 448, 28, 17,
29 = 3,746 distinct, no overlap), filtered by a port of `belongs()` (lookup.go:213-228) for each PR.
(b) Per PR, three live calls: E1 (`--metadata-field repo= --metadata-field pr_number=`), E2
(`source_type=pr`, `source_id=<repo>#<n>`), E3 (`--title-contains <repo>#<n>`), all with `--all`.

PR set: the 118 PRs that carry a decider-minted bead (a parseable `dedup_key`). All 118 keys are
node-id form (`pr:PR_kwDO...:kind`), none is `repo#n` form except the 29 focus-items. No bead carries
`node_id` metadata, so a PR's node id is taken from the key ident of its rows; the 7 children without
metadata resolve to their PR through the shared node ident of the anchor (0 unresolved). 1,554 PRs
appear in work-items-all overall; the other 1,436 are old keyless `merge-request` PRs (see the
offline simulation below).

## Result 1: difference table (decider-minted PRs, live, 118 PRs)

Items the decider selects that E1+E2 do not return:

| difference cause                                                                                                                                      | bead count | example ids                                      |
| ----------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------------------------------------------------ |
| node_id-only key ident, keyed child with NO repo/pr_number metadata (the 7 process-feedback children D4 backfills) [E3 finds all 7]                   | 7          | zr-2nkwm, zr-4a8us, zr-98w64, zr-9dul4, zr-lgpkg |
| stale repo/pr_number metadata, case variant, leading-zero variant, rename, partial metadata, pr_number string-vs-number, focus-item without source_id | 0          |                                                  |

The 7 children and their PRs (all ZR-Private/ziprecruiter): zr-r9mzw #108278, zr-98w64 #112995,
zr-lgpkg #113250, zr-2nkwm #113324, zr-4a8us #113368, zr-rtejf #113441, zr-9dul4 #113446. With E3
added the gap is 0 for all 118 PRs (so also 0 in the BuildIndex-relevant subset).

Counts that explain the zeros: `pr_number` is a JSON number on 1,797 work-items-all rows and a string
on 742; `--metadata-field pr_number=N` matched both. All 414 keyed rows that carry repo+pr_number
(282 closed, 132 open) were returned by E1; the only keyed rows lacking it are the 7 above.

Extra rows (not selected by the decider today):

| kind                                                                                                   | rows | example ids                                                |
| ------------------------------------------------------------------------------------------------------ | ---- | ---------------------------------------------------------- |
| belongs() passes, not in work-items-all (ordinary bugs/tasks carrying repo/pr_number; E1 returns them) | 70   | zr-0kwc0.3, zr-0kwc0.4, zr-0kwc0.5, zr-0kwc0.6, zr-0kwc0.7 |
| raw E3 row rejected by belongs() (title substring false positive)                                      | 3    | zr-a7qkr, zr-u0w0p.3, zr-v6tqi                             |

## Result 2: raw row counts per PR (118 PRs, all 6-digit numbers)

| expression            | max | median | total |
| --------------------- | --- | ------ | ----- |
| E1                    | 50  | 2      | 484   |
| E2                    | 1   | 0      | 29    |
| E3                    | 23  | 2      | 453   |
| union after belongs() | 51  | 2      | 520   |

Per-PR values: `out/per-pr.json`. Compare 3,746 rows per lookup today.

## Result 3: E3 row bound by PR-number length

E3 matches the substring `<repo>#<N>`, so it also returns every PR whose number starts with N. Offline
over all 6,725 beads (every `<repo>#<digits>` title reference, counting rows per digit prefix), the worst
prefix per length, cross-checked live at 6 digits (max 23, #122377):

| digits | worst prefix | rows  | prefixes over 100 rows |
| ------ | ------------ | ----- | ---------------------- |
| 1      | #1           | 3,380 | 2                      |
| 2      | #10          | 1,325 | 6                      |
| 3      | #109         | 204   | 18                     |
| 4      | #1223        | 54    | 0                      |
| 5      | #12237       | 49    | 0                      |
| 6      | #122377      | 23    | 0                      |

Decision on the bound (proposal 100 rows): send E3 only when the PR number has at least 4 digits. At 4
digits the observed worst case is 54 rows (46 under the bound); at 3 digits 18 prefixes already exceed 100. The corpus only grows, so 4 digits is safe today with margin of about 2x, not forever; the check
belongs in the same flake-check/measurement as the other D3 numbers. PRs with 1 to 3 digits are
ancient (this repo is past #127000) and rely on E1+E2 plus the consistency check.

## Result 4: is E3 needed, and are node_id-only PRs real?

- E3 is needed. (i) Until the backfill (bead .7) runs, E1+E2 miss 7 PRs' process-feedback children and
  only E3 finds them. (ii) After it, E3 is still the only expression that finds keyless legacy items
  without repo/pr_number metadata: offline simulation over all 1,554 PRs shows 1,169 such beads on 1,022
  PRs (all closed legacy children found by title) that E1+E2 cannot return; no other cause appears.
  Those PRs carry no decider key, so the decider would mint a duplicate anchor unless E3 or the
  consistency check covers them. (Simulation models E1/E2 as exact metadata equality over the six
  expressions' rows, validated by the live run on the 118 PRs: 0 mismatches.)
- A PR identified only by node_id does not exist in this data: 0 of the 118 have a key whose PR cannot
  be tied to repo/pr_number (via metadata or the shared ident). The 7 children ARE node-keyed rows with
  no metadata, but their anchor carries the metadata and E3 finds them by title, so no extra expression
  is needed. The fail-closed, documented behavior for the hypothetical case stays correct.

## Result 5: E1+E2 sufficient?

No, not today: they miss 7 of 118 decider-minted PRs (the 7 unbackfilled children). After the D4
backfill they are sufficient for every decider-minted PR, but not for the 1,022 legacy keyless PRs
(1,169 beads), which need E3 (numbers of 4+ digits, which all of them are in practice) or the consistency
check. PRs needing E3 now: #108278, #112995, #113250, #113324, #113368, #113441, #113446 (all
ZR-Private/ziprecruiter). PRs needing the consistency check (not found by E1+E2+E3): none among the 118.

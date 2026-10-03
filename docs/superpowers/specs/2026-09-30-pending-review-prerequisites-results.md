# Pending-review prerequisites P1 to P8: experiment results

- **Date**: 2026-09-30 (run on 2026-09-30 UTC)
- **Bead**: `pg2-kftf9.11` (experiment); design under test: `2026-09-29-pending-review-handling-investigation.md` (bead `pg2-kftf9.10`), section "Prerequisite proof plan".
- **Verdict summary**: the technical mechanics the design relies on are PROVEN (P2, P4, P5, P7, P8 PASS; P1 and P3 PASS on the API-observable parts). The 2026-09-30 run left three gaps (G1 to G3) because they needed something that run was not authorized to use: a second GitHub identity (P1 part b), a human web-UI edit (P3 UI part), and fine-grained tokens (P6). All three were closed by the operator on 2026-10-02, and the outcomes are recorded under "Gap closure (2026-10-02)": G1 confirmed, G2 measured (and it produced two new design corrections), G3 closed by an operator decision that is NOT a measurement of fine-grained tokens.

## Method and environment

- Throwaway PRIVATE repository `pending-review-scratch` under the single authorized GitHub account, using the existing `gh` login (classic OAuth token, scopes `gist`, `read:org`, `repo`). No other account, org, repo, or host was touched. The PR author and the reviewer were the same identity (the same shape as the router worker, which posts under the operator's own account).
- Setup: commit `base` on `main`; branch `feat` with commit H1 (`a.txt`, four lines) and draft PR `#1`; later commit H2 appended a fifth line.
- Reviews were created with the REST create-review endpoint (marker `<!-- pg-pr -->` stamped by hand to match `marker.HTMLMarker`; `marker.Stamp` itself was not invoked) and GraphQL. Raw responses were captured to a local scratch directory (not committed: they contain account logins and node ids). Commands and the essential response fields are quoted below.
- The scratch repo could NOT be deleted at the time: `gh repo delete` was refused with HTTP 403 ("needs the `delete_repo` scope"). Per the bead's instruction the scope was not refreshed. **The repo is intentionally KEPT for future tests and verifications** (operator ruling, Phillip, 2026-10-02: "the scratch repo does not need to be removed. it can stay for future tests and vrerifications.").

## Results

| ID  | Verdict                                                       | One-line finding                                                                                                                                                                                 |
| --- | ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| P1  | PASS for (a) and (c); part (b) CONFIRMED by the operator (G1) | Both REST and GraphQL list the author's own PENDING review with id, commit, state. Author-only visibility could not be tested without a second account.                                          |
| P2  | PASS                                                          | Author can delete a pending review via REST and GraphQL; a submitted review cannot be deleted (422); the submit/delete race has exactly one winner and the loser gets a clear 422/404.           |
| P3  | PASS for marker round-trip; UI-edit part MEASURED (G2)        | Marker round-trips on body and every comment in REST and GraphQL. Marker removal and unmarked additions are detectable; a text-only edit that keeps the marker is NOT detectable except by hash. |
| P4  | PASS                                                          | A review keeps its anchor commit when the PR head advances; a review created without `commit_id` anchors to the current head.                                                                    |
| P5  | PASS (observation recorded)                                   | A thread added to an old pending review anchors to the CURRENT head, while the review itself stays anchored to the old commit.                                                                   |
| P6  | classic `repo` PASS; fine-grained WAIVED by the operator (G3) | Every call works with the classic `repo` scope. The read-only vs write fine-grained matrix was not run.                                                                                          |
| P7  | PASS                                                          | All five mutations exist with the assumed inputs; live calls returned the expected fields.                                                                                                       |
| P8  | PASS                                                          | After a force-push removes the review's commit, the pending review stays listed, its comments become `outdated`, DELETE still succeeds, and submit as `COMMENT` also still succeeds.             |

### P1: visibility of the author's own pending review

Created R1 with `POST /repos/{o}/{r}/pulls/1/reviews` (`commit_id=H1`, marker-stamped body, two `comments[]`, no `event`). Response: `state=PENDING`, `commit_id=H1`, `submitted_at=null`.

- (a) `GET /repos/{o}/{r}/pulls/1/reviews` returned R1 with `state=PENDING`, `commit_id=H1`, `submitted_at=null`, the full body. It did NOT include inline comments; those need `GET .../reviews/{id}/comments`. PASS.
- (c) GraphQL `pullRequest(number:1){ reviews(first:50, states:[PENDING]){ nodes{ id databaseId state author{login} commit{oid} body comments(first:50){ nodes{ body } } } } }` returned the same review in one round trip: `databaseId` equals the REST `id` (5360090761), `commit.oid=H1`, body and both comment bodies. PASS.
- (b) The 2026-09-30 run could not test this: it needs a second, different identity. Closed as gap G1 (see "Gap closure (2026-10-02)").

Design meaning: the structured lookup (bead `pg2-kftf9.12`) can be built on GraphQL alone (one query returns id, commit, body, and per-comment bodies) or on REST list plus the review-comments endpoint. REST `line` is `null` for pending-review comments (only `position` and `original_position` are populated), so REST is not a drop-in for line-based logic.

### P2: delete semantics and race

- (a) `DELETE .../reviews/{id}` on pending R1: HTTP 200. Afterwards REST list is empty, GraphQL `states:[PENDING]` is empty, `GET .../reviews/{id}/comments` is 404, and the PR review-comment list is empty (comments gone with the review). PASS.
- (b) Created R2, submitted with `POST .../events -f event=COMMENT` (state became `COMMENTED`), then `DELETE`: HTTP 422 `Can not delete a non-pending pull request review`; R2 remained. GraphQL `deletePullRequestReview` on the same submitted review: `UNPROCESSABLE`, same message. PASS.
- (c) Race. Three trials with submit and delete launched concurrently: delete won every time (HTTP 200); submit lost with 422 (`Could not resolve to a node with the global id ...`) or 404. Three further trials where submit ran first: submit won and delete lost with 422 `Can not delete a non-pending pull request review`, review left `COMMENTED`. Those three second-batch trials also issued a second submit inside the delete branch, so they demonstrate submit-then-delete semantics rather than a clean simultaneous race. Across all trials, exactly one operation won and the loser got a 4xx. PASS.
- (d) GraphQL `deletePullRequestReview(input:{pullRequestReviewId:$id})` on a pending review returned `pullRequestReview{id state}` (state shown as the pre-delete `PENDING`) and the review was gone from both lists. PASS.
- Also observed: creating a second pending review while one exists returns HTTP 422 `User can only have one pending review per pull request`. This is the wedge condition the design addresses.

Design meaning: the delete-and-recreate path is executable. A delete that loses a race with a human submit surfaces as HTTP 422 with `non-pending`; the design's policy of treating that as `blocked_human_pending` with reason `delete_refused` after one re-list is correct. Delete returns 200 with a body (not 204), so callers MUST not assume an empty response.

### P3: marker detection and edit detection

- Marker round-trip: body and both comments, read back through REST (`/reviews`, `/reviews/{id}/comments`) and GraphQL, all carried `<!-- pg-pr -->` verbatim. PASS.
- Edits were made programmatically with the same token instead of the web UI (see G2): GraphQL `updatePullRequestReviewComment` to remove the marker from comment 1; the same mutation to keep the marker but change the text of comment 2; `addPullRequestReviewThread` to add a fifth, unmarked comment. Read-back showed: the marker-removed comment and the unmarked addition are plainly detectable (no marker); the text-only edit still carries the marker and is indistinguishable from an untouched comment by marker alone.
- Additional signals observed: REST comment `updated_at` differed from `created_at` for the two edited comments and was equal for untouched ones; GraphQL `lastEditedAt` stayed `null` for API edits and `includesCreatedEdit` was `false`. Whether a web-UI edit populates `lastEditedAt` was answered by G2: it does not (see "Gap closure (2026-10-02)").

Design meaning: confirms the risk section's premise. The marker guard catches removal and additions; only the post-time hash (bead `pg2-kftf9.14`) catches a marker-preserving text edit. REST `updated_at != created_at` MAY be used as a cheap extra tripwire but MUST NOT replace the hash, since its behavior under non-edit updates (for example anchor shifts after a push) was not isolated.

### P4: anchored commit vs PR head

With R1 anchored at H1, H2 was pushed. Review-level `commit_id` (REST) and `commit.oid` (GraphQL) both still reported H1 while the head was H2. After deleting R1, a review created WITHOUT `commit_id` reported `commit_id=H2`. PASS.

Observations that affect implementation:

- REST `GET /pulls/{n}` `head.sha` lagged the push by several seconds on two occasions (it still returned the old head while GraphQL `headRefOid` already returned the new one). A staleness check that compares a review commit to the head SHOULD read the head from GraphQL `headRefOid` or the stage sidecar, or tolerate the lag; a lagging REST head can make a stale review look current.
- Per-comment `commit_id` is NOT stable: after H2 was pushed and a thread was added, the earlier comments reported `commit_id=H2` and `original_commit_id=H1`. The stale check MUST use the review-level commit, never a comment's `commit_id`.

### P5: reuse-thread anchoring

With R1 pending at H1 and head at H2, `addPullRequestReviewThread` on R1 for an H2-only line (`a.txt` line 5, `side:RIGHT`) succeeded. The thread's comment reported `commit.oid=H2` and `originalCommit.oid=H2`, `isOutdated=false`. A thread on a line present in both commits also anchored to H2. The review itself still reported `commit_id=H1`.

Design meaning (section 4.1 decision): reuse is technically feasible, because new threads target the current head. But the review then mixes comments anchored to H1 and H2 under a review-level commit of H1, so the commit-equality guard (`R1.commit == head`) would misreport an extended review as stale or, after a same-commit re-run, as current. Stale H1 findings also remain (this run did not find or test a per-comment delete mutation). The design's verdict that reuse is not the default stands, now on evidence.

### P6: token scope

Only the classic OAuth token with scope `repo` was available. With it: list, create, update (`PUT`), delete, submit, and every GraphQL mutation succeeded (see P1, P2, P7). The response header `X-Accepted-Oauth-Scopes` was empty for create and `X-Oauth-Scopes` was `gist, read:org, repo`. The fine-grained matrix (`Pull requests: read` only, then `write`) and the actual router-worker token could not be exercised: minting or reading another token was outside the authorization. Recorded as gap G3, closed by an operator decision (see "Gap closure (2026-10-02)").

### P7: GraphQL mutation details

Introspection results (`__type(name:...){inputFields{...}}`):

| Mutation                     | Input fields (required marked `!`)                                                                                               |
| ---------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `deletePullRequestReview`    | `pullRequestReviewId: ID!`                                                                                                       |
| `submitPullRequestReview`    | `pullRequestReviewId: ID`, `pullRequestId: ID`, `event: PullRequestReviewEvent!`, `body: String`                                 |
| `updatePullRequestReview`    | `pullRequestReviewId: ID!`, `body: String!`                                                                                      |
| `addPullRequestReviewThread` | `pullRequestReviewId: ID`, `pullRequestId: ID`, `path`, `line`, `side`, `startLine`, `startSide`, `subjectType`, `body: String!` |
| `addPullRequestReview`       | `pullRequestId: ID!`, `commitOID`, `body`, `event`, `comments`, `threads`                                                        |

`PullRequestReviewEvent` values: `COMMENT`, `APPROVE`, `REQUEST_CHANGES`, `DISMISS`. Live calls, all successful: `addPullRequestReview` (with `commitOID` and one inline thread, no `event`) returned `state=PENDING`, `commit.oid` equal to the supplied SHA, and the thread comment; `updatePullRequestReview` changed the body and left the inline comment untouched; `submitPullRequestReview(event:COMMENT)` returned `state=COMMENTED`; `deletePullRequestReview` and `addPullRequestReviewThread` were exercised in P2 and P5. `submitPullRequestReview` without `event` is rejected at validation (`missingRequiredInputObjectAttribute`), and REST submit without `event` returns HTTP 422. REST `PUT .../reviews/{id}` updates only the body. PASS.

### P8: force-push behavior

Scenario A: R pending at H2 with two comments (one on an H2-only line); `feat` was force-pushed (`--force-with-lease`) to a commit that does not contain H2. Result: the review stayed listed in REST and GraphQL with its review-level `commit_id` unchanged (the removed H2); both comments were still returned, GraphQL `outdated=true` for both, REST `position` reset (`position=1`, `original_position` preserved as 2 and 5). `DELETE` returned HTTP 200 and the review was gone.

Scenario B (separate copy): pending review at the then-current head, second force-push removing that head. Same listing behavior (one comment `outdated=true`, one `false`). `submit` as `COMMENT` succeeded (`state=COMMENTED`, `commit_id` unchanged).

PASS: the required property (DELETE still succeeds after a force-push) holds. A force-push does NOT create an unremovable pending review.

## Gaps and what they mean (as found 2026-09-30; closed below)

| Gap | Missing proof                                                                                                                      | Why not done                                                                                                                  | Design consequence until closed                                                                                                                                                                                       |
| --- | ---------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| G1  | P1(b): a second identity does NOT see the author's pending review                                                                  | Only one GitHub identity is authorized                                                                                        | The design does not depend on other users' view, but "author-only" remains documented behavior, not observed. Needs a human or a second authorized account.                                                           |
| G2  | P3 UI part: web-UI edit of a pending comment, plus whether a UI edit sets `lastEditedAt`, and the "Finish your review" observation | An agent cannot use the web UI                                                                                                | API-simulated edits behaved as described. A human SHOULD repeat the three edits once in the web UI and record `lastEditedAt`/`updated_at`. The hash guard is the only marker-preserving-edit defense in the meantime. |
| G3  | P6: fine-grained PAT matrix (`Pull requests: read` vs `write`) and the router worker's actual token class                          | Minting or inspecting other tokens is outside the authorization; fine-grained PATs cannot be created through this login's API | The minimal grant is unrecorded. Before implementation a human MUST run the P6 matrix with the real worker token; the classic `repo` scope is proven sufficient for every call.                                       |

The bead's operator ruling says all prerequisites MUST be proven. G1, G2, and G3 require a human (or explicit authorization for a second account and token minting) and SHOULD be filed as a human bead. Whether the dependent implementation beads may start before G1/G2 close is an operator decision: none of P2, P4, P7, P8 (which gate the replace path) depends on them. G3 gates only token-grant documentation and the operator verbs' permission notes.

## Gap closure (2026-10-02)

Closed by the operator in a live session. Bead `pg2-6nzpx`.

### G1: a second identity does not see the author's pending review

The operator confirmed that a second GitHub identity does NOT see the author's pending review. This is the operator's observation; no raw output was captured here. P1 part (b) is therefore closed as "author-only visibility confirmed".

### G2: web-UI edit of a pending comment

Method: a fresh pending review (three marker-stamped comments, A, B and C) was created through the API on the scratch PR. The operator then edited it in the web UI: A untouched, B text edited with the marker line kept, C marker line removed. The review was never submitted. Before and after snapshots were taken through GraphQL and REST.

| Signal                                      | A (untouched)    | B (text edited, marker kept) | C (marker removed) |
| ------------------------------------------- | ---------------- | ---------------------------- | ------------------ |
| GraphQL `lastEditedAt`                      | `null`           | `null`                       | `null`             |
| GraphQL `includesCreatedEdit`               | `false`          | `false`                      | `false`            |
| `updatedAt` (GraphQL) / `updated_at` (REST) | equal to created | moved forward                | moved forward      |
| Marker present                              | yes              | yes                          | no                 |
| Newlines in the stored body                 | LF               | CRLF                         | not applicable     |

Findings:

- `lastEditedAt` is `null` for a web-UI edit of a pending comment, exactly as for an API edit. It MUST NOT be used as an edit signal.
- `updated_at` moves on a UI edit and equals `created_at` for an untouched comment. It remains a cheap extra tripwire only (see P3); the earlier caveat about non-edit updates still applies.
- Saving a UI edit rewrites the comment's newlines from LF to CRLF. Any post-time hash comparison MUST normalize `\r\n` to `\n` before hashing and comparing.
- The marker guard catches C. B keeps the marker and is caught only by the hash, as predicted.
- Not measured: whether opening a comment in the editor and saving it with NO change also alters the stored body (the CRLF conversion would then make a no-op save look like an edit).
- "Finish your review" is the dialog GitHub's UI opens when the author clicks "Submit review". It is the submit dialog; the operator reported no other pending-state observation.

### G3: token class

What the code does: pg-pr resolves its GitHub token from `GH_TOKEN` or `GITHUB_TOKEN` in the environment, and otherwise from `gh auth token` (`packages/pg-pr/pkg/provider/vcs/github/token.go`, `defaultTokenSource`). Whether the router worker's launchd environment sets either variable was not checked.

Decision (operator, 2026-10-02): a SINGLE read/write token is used. The read-only versus read/write fine-grained matrix was NOT run, and no fine-grained token was exercised. What is proven is unchanged: the classic OAuth token with scope `repo` is sufficient for every call in P1, P2, P7 and P8. The operator waived the fine-grained matrix. If a fine-grained token is adopted later, the calls to exercise are: list reviews (REST and GraphQL), create a pending review, update its body, add a thread, delete it, and create then submit one.

## Design corrections to carry into the dependent beads

1. Stale check: use the review-level commit (`commit_id` or `commit.oid`) against GraphQL `headRefOid`; never a comment-level commit; tolerate REST `head.sha` lag.
2. A review that was extended (P5) has mixed comment anchors; if reuse is ever offered, the commit-equality guard is unreliable for it.
3. Marker guard alone is insufficient for text-only edits; the post-time hash is mandatory (confirmed).
4. Treat HTTP 422 with `non-pending` (REST) or `UNPROCESSABLE` (GraphQL) on delete as `delete_refused`; a lost race yields exactly this.
5. Pending review comments are visible in REST only via `/reviews/{id}/comments` and report `line=null`; prefer GraphQL for the structured lookup.
6. The post-time hash MUST be computed over newline-normalized text (`\r\n` to `\n`): a web-UI edit stores CRLF (G2).
7. `lastEditedAt` is NOT an edit signal for pending comments (`null` after a web-UI edit, G2); do not build the edit tripwire on it.

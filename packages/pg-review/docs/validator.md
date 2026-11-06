# Findings validator and review-item renderer

`internal/validate` is the gate `agent file` uses; `internal/render` produces the text
`human show` prints. Both read only what a run left behind: the evidence rows
(`<evidence dir>/evidence.json`), the query log (`query-log.jsonl`), the persisted query results
(`query-results/Q-nnn.json`, a JSON array of rows indexed from 0) and the staged raw text.
`validate.Load` reads them; a caller fills in the decoded document, the lens kind list and the
limits.

## Result

`validate.Validate` returns nil, or an `*Error` carrying every failure (a batch is validated whole:
one invalid finding rejects it). Each failure names the finding (its `subject_key`, or `#<index>`),
the field, the rule and the offending token. `Error.ExitCode()` is the exit-4 class.

## Rules

| Rule                           | Meaning                                                                                                                               |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------- |
| `unknown-kind`                 | `kind` is not in the lens's closed list                                                                                               |
| `unknown-subject`              | `subject_key` is in no evidence row                                                                                                   |
| `missing-section`              | a required section is empty (title, category, situation, history, problem, why, impact, recommendation, an option part, evidence ids) |
| `options-incomplete`           | fewer than two options, or none named "Do nothing" or "Leave it"                                                                      |
| `missing-confirm-by`           | `confirm_by` is empty                                                                                                                 |
| `field-too-long`               | a field exceeds `field_max_chars` (default 1200 characters)                                                                           |
| `unknown-evidence-id`          | an `E-` or `Q-` id (in `evidence` or cited in text) does not exist; a `Q-` id needs a query log entry AND a result file               |
| `unknown-metric`               | a cited metric key does not exist                                                                                                     |
| `count-mismatch`               | a cited value differs from the evidence                                                                                               |
| `sum-addend-not-in-evidence`   | an addend is not a metric value of a cited evidence row or a value of a cited query result                                            |
| `sum-mismatch`                 | the addends do not add to the total (recomputed)                                                                                      |
| `undefined-glossary-term`      | a token needing a definition is in neither the finding's glossary nor the allowlist                                                   |
| `raw-text-overlap`             | a field shares too many n-grams with the staged raw text                                                                              |
| `url-copied-from-evidence`     | a URL of a field also occurs in the evidence or raw text                                                                              |
| `command-copied-from-evidence` | a shell-looking backticked or fenced string of a field also occurs in the evidence or raw text                                        |
| `too-many-findings`            | more findings than the cap                                                                                                            |

## Citation syntax (chosen here)

Counts are checked where the analyst cites them: `[E-001.errors=6]` says evidence row `E-001`
has metric `errors` equal to 6; `[Q-001#0.errors=10]` says row 0 of the persisted result of
`Q-001` has `errors` equal to 10. Without `=n` the reference is only checked to exist. A sample
reference is the evidence id plus a letter (`E-001a` is the first sample of `E-001`); a letter
past the row's samples is an unknown id.

## Choices recorded

- Raw-text overlap: 8-word n-grams over lowercased words; a field is rejected when more than 20 per
  cent of its n-grams (fields under 8 words have none) also occur in the staged raw text
  (`OverlapThreshold`).
- Glossary tokens: id patterns (`[A-Z]+-\d+`, an `E-nnnx` sample letter is dropped), `pg-` names,
  backticked strings, whole words on `internal/builtin/assets/vocabulary.txt` (case-insensitive),
  and the capitalized state words in `StateWords` used mid-sentence (a sentence-initial one reads
  as ordinary English). A token is defined by a glossary term (case-insensitive, backticks
  ignored), by the finding's own evidence ids, or by `internal/builtin/assets/allowlist.txt`.
- A "command" is a backticked or fenced string of two or more words whose first word looks like a
  command name (lowercase letters, digits and `._/~-`).
- Stripping (`render.Strip`) removes C0 and C1 control characters and ANSI escape sequences, and
  keeps newline and tab.

## Golden item

`testdata/review-item/` holds a synthetic evidence set, raw text, findings document, header
facts (`item.json`) and the expected rendering (`item.golden`). Regenerate the golden with
`PG_REVIEW_UPDATE_GOLDEN=1 go test ./internal/render/`. The test requires the findings to pass
the validator and the rendered text to have no undefined term.

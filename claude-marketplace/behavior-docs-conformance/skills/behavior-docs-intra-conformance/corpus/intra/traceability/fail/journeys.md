# Use cases & journeys — traceability FAIL fixture

Three traceability defects, one per section below: a story with no listing, an invariant in no
listing, and a listed name that resolves to nothing.

## User stories

- **`STORY-1`** <!-- uuid: 55555555-5555-4555-8555-555555555555 --> — As an operator, I want each
  accepted event delivered, so no work is lost. _(→ `USECASE-1`; `INV-1`, `INV-99`.)_
- **`STORY-2`** <!-- uuid: 66666666-6666-4666-8666-666666666666 --> — As an operator, I want a
  retried submission absorbed rather than duplicated.

## Use cases

### `USECASE-1` — Submit an event <!-- uuid: 77777777-7777-4777-8777-777777777777 -->

_Primary actor:_ the operator. _Level:_ **user-goal**.
_Requires:_ `INV-1`.

Submit the event and observe it delivered.

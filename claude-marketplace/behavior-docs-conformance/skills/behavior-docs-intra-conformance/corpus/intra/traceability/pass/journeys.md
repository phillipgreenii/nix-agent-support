# Use cases & journeys — traceability PASS fixture

Every element carries a listing, every invariant appears in one, and every listed name resolves.

## User stories

- **`STORY-1`** <!-- uuid: 55555555-5555-4555-8555-555555555555 --> — As an operator, I want each
  accepted event delivered, so no work is lost. _(→ `USECASE-1`; `INV-1`.)_
- **`STORY-2`** <!-- uuid: 66666666-6666-4666-8666-666666666666 --> — As an operator, I want a
  retried submission absorbed rather than duplicated. _(→ `USECASE-1`; `INV-2`.)_

## Use cases

### `USECASE-1` — Submit an event <!-- uuid: 77777777-7777-4777-8777-777777777777 -->

_Primary actor:_ the operator. _Level:_ **user-goal**.
_Requires:_ `INV-1`, `INV-2`.

Submit the event and observe it delivered.

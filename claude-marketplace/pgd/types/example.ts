// The one documented example contributor. It is compiled against the types contract by the check
// test-pgd-types (tsc --noEmit over this directory); it is not loaded by the mod.
import type { PgdNoun } from './pgd'

// A bundle pushes its state whenever it changes and removes the items when the state clears.
// ttlMs makes a contribution disappear on its own when the bundle stops refreshing it.
export async function publish(pgd: PgdNoun, state: { blocked: number; focus?: string }): Promise<void> {
  if (state.blocked > 0) {
    await pgd.put({ id: 'example:blocked', surface: 'alert', priority: 65, text: `${state.blocked} blocked`, ttlMs: 60_000 })
  } else {
    await pgd.remove({ id: 'example:blocked' })
  }
  if (state.focus) {
    await pgd.put({
      id: 'example:focus',
      surface: 'focus',
      priority: 10,
      text: state.focus,
      color: 'green',
      url: 'https://example.com/work',
      ttlMs: 120_000,
    })
  }
}

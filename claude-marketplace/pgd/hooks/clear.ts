import { COMPACTION_ID, refreshCompaction } from './compaction'
import { resetLimitsCapture } from './limits-capture'
import { pgdEnabled } from './options'
import { SESSION_NAME_ID, resetSessionName } from './session-name'
import { clearValue, noteEvent, refresh, requestInvalidate, setValue } from './source'
import type { ModApi } from './types'
import { mapUsage } from './usage'

// /clear and an in-process /resume: the conversation ends and the process goes on under a NEW
// session id, with no session.start. The only signal is session.end with reason 'clear' or
// 'resume'. Every value that belongs to the old conversation is dropped at once (so the band does
// not keep the old context percent, name, mode or compaction note until the next turn), then read
// again once the new session exists.
//
// The new id is not known while session.end runs ($.session.id() answers the old one until the
// engine switches), so the reads wait for it: each timer asks for the id and reads only when it
// differs from the ending session's. A source that reads by session id (the name, the mode, the
// compaction marker) would otherwise re-read the OLD session's data. When the id never changes
// within the delays, the values stay cleared (blank, never stale) and the next turn fills them.
export const CLEAR_RETRY_DELAYS_MS = [300, 1000, 2000, 5000] as const

const ENDING_REASONS: ReadonlySet<string> = new Set(['clear', 'resume'])

// Sources whose value is about the conversation, not the machine, the repo or the environment.
const SESSION_SCOPED = ['usage', SESSION_NAME_ID, 'sessionMode', COMPACTION_ID, 'lastCompaction'] as const

export function clearSessionValues(): void {
  resetSessionName()
  resetLimitsCapture()
  for (const id of SESSION_SCOPED) clearValue(id)
  requestInvalidate()
}

// Every read that needs no `$`, run once the new session id is known. The branch and the model do
// not change with the conversation, so they are not read again.
export function refreshSessionValues(): void {
  noteEvent()
  // The limits capture may have been re-seeded from the old file between the clear and now.
  resetLimitsCapture()
  refresh(SESSION_NAME_ID, { force: true })
  refresh('sessionMode', { force: true })
  refreshCompaction()
}

export function registerClearHooks(on: ModApi): void {
  on('session.end', async ($: ModApi, e: { reason?: string; sessionId?: string }, next: (e: unknown) => Promise<unknown>) => {
    const applies = pgdEnabled() && ENDING_REASONS.has(String(e?.reason))
    // Clear before the engine's end step: the end chain shares one short wall-clock bound.
    if (applies) clearSessionValues()
    const result = await next(e)
    if (!applies) return result
    const oldId = String(e?.sessionId ?? '')
    const attempt = (i: number): void => {
      $.clock.after(CLEAR_RETRY_DELAYS_MS[i], async () => {
        if (!pgdEnabled()) return
        let id = ''
        try {
          id = String((await $.session.id()) ?? '')
        } catch {
          // an unreadable id counts as "not yet"
        }
        if (id === '' || id === oldId) {
          if (i + 1 < CLEAR_RETRY_DELAYS_MS.length) attempt(i + 1)
          return
        }
        refreshSessionValues()
        try {
          setValue('usage', mapUsage(await $.session.usage()))
        } catch {
          // keep the cleared value; the next session.measure fills it
        }
      })
    }
    attempt(0)
    return result
  })
}

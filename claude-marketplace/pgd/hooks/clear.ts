import { COMPACTION_ID, refreshCompaction, resetCompactionRead } from './compaction'
import { resetSessionFacts } from './info-modal'
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
// compaction marker) would otherwise re-read the OLD session's data. A polled source may still
// start a fetch inside that window, so the sources are cleared a second time when the id switches,
// which also discards such a fetch. When the id never changes within the delays, the values stay
// cleared (blank, never stale) and the next turn.complete or session.measure fills them.
export const CLEAR_RETRY_DELAYS_MS = [300, 1000, 2000, 5000] as const

const ENDING_REASONS: ReadonlySet<string> = new Set(['clear', 'resume'])

// Sources whose value is about the conversation, not the machine, the repo or the environment.
// The model and effort are left alone: they follow the settings, not the conversation (an
// in-process /resume into a session that used another model is refreshed with the model read).
const SESSION_SCOPED = ['usage', SESSION_NAME_ID, 'sessionMode', COMPACTION_ID, 'lastCompaction'] as const
// The id-keyed sources the id switch reads again (usage is set from $.session.usage() instead).
const ID_KEYED = [SESSION_NAME_ID, 'sessionMode', COMPACTION_ID] as const

export function clearSessionValues(): void {
  resetSessionName()
  resetLimitsCapture()
  resetCompactionRead()
  resetSessionFacts()
  for (const id of SESSION_SCOPED) clearValue(id)
  requestInvalidate()
}

// Every read that needs no `$`, run once the new session id is known.
export function refreshSessionValues(): void {
  noteEvent()
  // The limits capture may have been re-seeded from the old file between the clear and now, and a
  // polled fetch may have started under the old id: drop both before reading.
  resetLimitsCapture()
  resetSessionFacts()
  for (const id of ID_KEYED) clearValue(id)
  refresh(SESSION_NAME_ID, { force: true })
  refresh('sessionMode', { force: true })
  refresh('model', { force: true })
  refreshCompaction()
}

export function registerClearHooks(on: ModApi): void {
  on('session.end', async ($: ModApi, e: { reason?: string; sessionId?: string }, next: (e: unknown) => Promise<unknown>) => {
    const applies = pgdEnabled() && ENDING_REASONS.has(String(e?.reason))
    let oldId = String(e?.sessionId ?? '')
    if (applies) {
      if (oldId === '') {
        try {
          oldId = String((await $.session.id()) ?? '')
        } catch {
          // without the ending id no switch can be recognised: nothing is read afterwards
        }
      }
      try {
        // Clear before the engine's end step: the end chain shares one short wall-clock bound.
        clearSessionValues()
      } catch {
        // a failing clear must not stop the engine's end step
      }
    }
    try {
      return await next(e)
    } finally {
      if (applies && oldId !== '') {
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
        try {
          attempt(0)
        } catch {
          // no timer: the values stay cleared until the next turn
        }
      }
    }
  })
}

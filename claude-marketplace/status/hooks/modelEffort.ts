import { displayEnabled } from './options'
import { defineSource, noteEvent, refresh, setValue } from './source'
import type { ModApi } from './types'

// The model and effort sources, both event-driven (intervalMs 0). model: read through
// host.session.model() at session start (refresh) and again after a config.set; effort: the
// turn.step event's effort for the main loop (a subagent's step is ignored), a string, undefined
// until the first turn. The turn.step hook is an async generator that delegates with yield* next(e).
export function startModelEffort(_host: ModApi): void {

  defineSource<string>({
    id: 'model',
    intervalMs: 0,
    feeds: ['band', 'modal'],
    fetch: ({ host: h }) => h.session.model(),
  })
  defineSource<string>({ id: 'effort', intervalMs: 0, feeds: ['band', 'modal'] })
  refresh('model')
}

export function registerModelEffortHooks(on: ModApi): void {
  on('config.set', async ($: ModApi, e: unknown, next: (e: unknown) => unknown) => {
    const r = await next(e)
    if (displayEnabled()) {
      noteEvent()
      try {
        setValue('model', await $.session.model())
      } catch {
        // keep the last cached model
      }
    }
    return r
  })
  on(
    'turn.step',
    async function* (
      _$: ModApi,
      e: { agentId?: string; effort?: string | number },
      next: (e: unknown) => AsyncGenerator<unknown, unknown>,
    ) {
      if (displayEnabled()) {
        noteEvent()
        if (e.agentId === undefined) {
          setValue('effort', e.effort === undefined ? undefined : String(e.effort))
        }
      }
      return yield* next(e)
    },
  )
}

import { pgdEnabled } from './options'
import { defineSource, noteEvent, refresh, setValueIfChanged } from './source'
import type { ModApi } from './types'

// The model and effort sources, both event-driven (intervalMs 0). Both are written through
// setValueIfChanged, so a nil value or one equal to the cached value changes nothing.
//   model: read through host.session.model() at session start (refresh), again when the main
//   loop's turn.step event names a different model than the previous step (which catches a /model
//   change on the next step), and again after a config.set.
//   effort: the main loop's turn.step `effort`, a string, undefined until the first turn.
// A subagent's step (it carries an agentId) is ignored for both. The turn.step hook is an async
// generator that delegates with yield* next(e).
// The model the last main-loop step named, as the step spells it; only compared with the next step.
let lastStepModel: string | undefined

export function startModelEffort(_host: ModApi): void {
  lastStepModel = undefined
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
    if (pgdEnabled()) {
      noteEvent()
      try {
        setValueIfChanged('model', await $.session.model())
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
      e: { agentId?: string; model?: string; effort?: string | number },
      next: (e: unknown) => AsyncGenerator<unknown, unknown>,
    ) {
      if (pgdEnabled()) {
        noteEvent()
        if (e.agentId === undefined) {
          // The step's model may be spelled differently from $.session.model() (a full id against
          // the name /model shows), so a change in it only triggers a read of $.session.model(),
          // which keeps the cached value in one spelling.
          if (e.model != null && e.model !== lastStepModel) {
            lastStepModel = e.model
            refresh('model', { force: true })
          }
          setValueIfChanged('effort', e.effort === undefined ? undefined : String(e.effort))
        }
      }
      return yield* next(e)
    },
  )
}

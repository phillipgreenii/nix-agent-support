// Tests of the Model and effort modal: the band button, the choices with the current one marked,
// Apply (config.set then a forced read of the model source), an effort choice (prompt.fill, no
// submit), the opt-out and the choices documented in the README.
import { expect, test } from 'claude-code/testing'
import { clearBandButtons, pressButton, renderBand } from './band'
import { registerComponents } from './components'
import { resetModals, setModalHost, pressAction, type ModalSpec } from './modal'
import { EFFORT_CHOICES, MODEL_CHOICES, modelEffortSpec } from './model-modal'
import { setOptions } from './options'
import { defineSource, initSources, readCached, setValue } from './source'
import { resetToggles } from './toggles'
import { DEFAULT_OPTIONS, type PgdOptions } from './types'
import { fakeClock, settle, stubHost } from './testkit'

const T = DEFAULT_OPTIONS.alerts.thresholds
const only = (modelEffort: boolean): PgdOptions => ({
  ...DEFAULT_OPTIONS,
  components: { ...DEFAULT_OPTIONS.components, beads: false, prStatus: false, sessionMode: false, modelEffort },
})

async function world(options = only(true)) {
  resetToggles()
  resetModals()
  clearBandButtons()
  const clock = fakeClock()
  const sh = stubHost()
  // a config.set of the model changes what session.model answers, as the real host does
  const baseSet = sh.host.config.set
  sh.host.config.set = async (a: { key: string; value: string }) => {
    await baseSet(a)
    if (a.key === 'model') sh.state.model = a.value
  }
  const opened: ModalSpec[] = []
  setModalHost({ open: s => void opened.push(s), close: () => undefined })
  setOptions(options)
  initSources(sh.host, { now: clock.now, setTimer: clock.setTimer })
  defineSource<string>({ id: 'model', intervalMs: 0, feeds: ['band', 'modal'], fetch: ({ host }) => host.session.model() })
  defineSource<string>({ id: 'effort', intervalMs: 0, feeds: ['band', 'modal'] })
  registerComponents(options, sh.host)
  setValue('model', 'sonnet')
  await clock.advance(1)
  return { clock, sh, opened }
}

const bandRow = (): { id: string; label: string; action?: string } | undefined =>
  renderBand({ sessionId: 'sess-1', model: readCached<string>('model'), effort: readCached<string>('effort') }, 200, T)
    .flat()
    .find(l => l.id === 'model')
const marked = (s: ModalSpec, label: string): string[] => s.items.filter(i => i.label === label && i.current).map(i => i.value)

test('the model label is a button that opens the modal with the current model marked and effort blank', async () => {
  const w = await world()
  expect(bandRow()).toEqual({ id: 'model', label: 'sonnet', action: 'model' })
  pressButton('model')
  await settle()
  expect(w.opened).toHaveLength(1)
  const s = w.opened[0]
  expect(s.title).toBe('Model and effort')
  expect(s.items.filter(i => i.label === 'model').map(i => i.value)).toEqual([...MODEL_CHOICES])
  expect(marked(s, 'model')).toEqual(['sonnet'])
  // effort is blank until the first turn: one empty item, none current
  expect(s.items.filter(i => i.label === 'effort')).toEqual([{ label: 'effort', value: '' }])
  expect(w.sh.host.session && w.sh.calls.includes('process.run')).toBe(false)
})

test('after the first turn the effort choices are listed with the current one marked', async () => {
  await world()
  setValue('effort', 'high')
  const s = modelEffortSpec()
  expect(s.items.filter(i => i.label === 'effort').map(i => i.value)).toEqual([...EFFORT_CHOICES])
  expect(marked(s, 'effort')).toEqual(['high'])
  expect(bandRow()?.label).toBe('sonnet · high')
  expect(s.actions.map(a => a.id)).toContain('effort:low')
})

test('a current model that is not a choice is appended and marked', async () => {
  await world()
  setValue('model', 'claude-custom-1')
  const s = modelEffortSpec()
  expect(marked(s, 'model')).toEqual(['claude-custom-1'])
  expect(s.items.filter(i => i.label === 'model')).toHaveLength(MODEL_CHOICES.length + 1)
})

test('Apply calls config.set with the model id, refreshes the model source and closes the modal', async () => {
  const w = await world()
  pressButton('model')
  await settle()
  pressAction('model', 'apply-model:opus')
  await settle()
  expect(w.sh.configSets).toEqual([{ key: 'model', value: 'opus' }])
  expect(readCached('model')).toBe('opus')
  expect(w.sh.fills).toEqual([])
})

test('an effort choice pre-fills /effort <level> and does not submit', async () => {
  const w = await world()
  pressButton('model')
  await settle()
  pressAction('model', 'effort:high')
  await settle()
  expect(w.sh.fills).toEqual([{ text: '/effort high', mode: 'replace' }])
  expect(w.sh.submits).toEqual([])
  expect(w.sh.configSets).toEqual([])
})

test('with components.modelEffort off there is no button, modal, fill or config change', async () => {
  const w = await world(only(false))
  expect(bandRow()?.action).toBe(undefined)
  pressButton('model')
  await settle()
  expect(w.opened).toHaveLength(0)
  pressAction('model', 'apply-model:opus')
  pressAction('model', 'effort:high')
  await settle()
  expect(w.sh.configSets).toEqual([])
  expect(w.sh.fills).toEqual([])
  expect(w.clock.pending()).toBe(0)
})

import { closeModal, registerModalBuilder, type ModalItem, type ModalSpec } from './modal'
import { readCached, refresh } from './source'
import type { ModApi, PgdOptions } from './types'

// The Model and effort component: the model label on the band becomes a button (action `model`,
// added in band.ts while components.modelEffort is on) that opens this modal. With the flag off
// startModelModal is never called, so there is no modal builder and no action handler.
//
// Choices made by the builder (documented in the package README, each covered by
// model-modal.test.ts):
//  - Model choices, in this order: sonnet, opus, haiku. Effort choices: low, medium, high, xhigh, max.
//  - Items: one per choice, labelled `model` or `effort`, value the choice; the current one carries
//    current: true. A current model that is not a choice is appended as an extra item. A blank
//    effort (before the first turn) is one item labelled `effort` with an empty value and no current.
//  - Actions: `apply-model:<id>` (label `Apply <id>`) and `effort:<level>` (label `Effort <level>`),
//    and no Close (the engine's x closes the pane).
//  - Apply passes the model id string: host.config.set({ key: 'model', value }); the cached model is
//    then read again with a forced refresh('model'), and the modal closes.
//  - An effort choice calls host.prompt.fill({ text: '/effort <level>', mode: 'replace' }) and never
//    submits; the modal closes so the prompt box is reachable.
export const MODEL_CHOICES: readonly string[] = ['sonnet', 'opus', 'haiku']
export const EFFORT_CHOICES: readonly string[] = ['low', 'medium', 'high', 'xhigh', 'max']
export const MODEL_MODAL_ID = 'model'
export const APPLY_PREFIX = 'apply-model:'
export const EFFORT_PREFIX = 'effort:'

export function modelEffortSpec(): ModalSpec {
  const model = readCached<string>('model')
  const effort = readCached<string>('effort')
  const items: ModalItem[] = MODEL_CHOICES.map(m => ({ label: 'model', value: m, ...(m === model ? { current: true } : {}) }))
  if (model && !MODEL_CHOICES.includes(model)) items.push({ label: 'model', value: model, current: true })
  if (effort === undefined || effort === '') {
    items.push({ label: 'effort', value: '' })
  } else {
    for (const e of EFFORT_CHOICES) items.push({ label: 'effort', value: e, ...(e === effort ? { current: true } : {}) })
    if (!EFFORT_CHOICES.includes(effort)) items.push({ label: 'effort', value: effort, current: true })
  }
  return {
    id: MODEL_MODAL_ID,
    title: 'Model and effort',
    items,
    actions: [
      ...MODEL_CHOICES.map(m => ({ id: `${APPLY_PREFIX}${m}`, label: `Apply ${m}` })),
      ...EFFORT_CHOICES.map(e => ({ id: `${EFFORT_PREFIX}${e}`, label: `Effort ${e}` })),
    ],
  }
}

export function startModelModal(_options: PgdOptions, host: ModApi): void {
  registerModalBuilder(MODEL_MODAL_ID, modelEffortSpec, async actionId => {
    if (actionId.startsWith(APPLY_PREFIX)) {
      const value = actionId.slice(APPLY_PREFIX.length)
      try {
        await host.config.set({ key: 'model', value })
      } catch {
        // a refused change is not shown; the read below keeps the model as it is
      }
      refresh('model', { force: true })
      closeModal(MODEL_MODAL_ID)
    } else if (actionId.startsWith(EFFORT_PREFIX)) {
      try {
        await host.prompt.fill({ text: `/effort ${actionId.slice(EFFORT_PREFIX.length)}`, mode: 'replace' })
      } catch {
        // a failed fill leaves the prompt box as it is
      }
      closeModal(MODEL_MODAL_ID)
    }
  })
}

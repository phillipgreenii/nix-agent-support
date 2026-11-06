// Tests of the modal framework: openModal and closeModal through the injectable host, the always
// present Close action, the pane calls of the default host (no holdToasts) and the drawing.
import { expect, test } from 'claude-code/testing'
import { closeModal, CLOSE_ACTION, drawModal, openModal, pressAction, resetModals, setModalHost, setPaneApi, type ModalSpec } from './modal'
import { isOpen, resetToggles } from './toggles'
import { initSources } from './source'
import { settle, stubHost } from './testkit'

const spec: ModalSpec = {
  id: 'demo',
  title: 'Demo',
  items: [
    { label: 'one', value: '1' },
    { label: 'two', value: '2', color: 'red', current: true },
  ],
  actions: [{ id: 'apply', label: 'Apply' }],
}

function recording() {
  const opened: ModalSpec[] = []
  const closed: string[] = []
  const handlers: Array<(a: string) => void | Promise<void>> = []
  setModalHost({
    open: (s, h) => {
      opened.push(s)
      handlers.push(h)
    },
    close: id => void closed.push(id),
  })
  return { opened, closed, handlers }
}

function fresh() {
  resetToggles()
  resetModals()
  initSources(stubHost().host)
}

test('openModal hands the host the spec with a Close action added', () => {
  fresh()
  const r = recording()
  openModal(spec, () => undefined)
  expect(r.opened).toHaveLength(1)
  expect(r.opened[0].actions.map(a => a.id)).toEqual(['apply', CLOSE_ACTION])
  expect(r.opened[0].items).toEqual(spec.items)
  expect(isOpen('demo')).toBe(true)
})

test('a spec that already has a Close action is not given a second', () => {
  fresh()
  const r = recording()
  openModal({ ...spec, actions: [{ id: CLOSE_ACTION, label: 'Done' }] }, () => undefined)
  expect(r.opened[0].actions).toEqual([{ id: CLOSE_ACTION, label: 'Done' }])
})

test('Close closes the modal and onAction never sees it; other actions reach onAction', async () => {
  fresh()
  const r = recording()
  const seen: string[] = []
  openModal(spec, a => void seen.push(a))
  await r.handlers[0]('apply')
  await r.handlers[0](CLOSE_ACTION)
  expect(seen).toEqual(['apply'])
  expect(r.closed).toEqual(['demo'])
  expect(isOpen('demo')).toBe(false)
})

test('closeModal of a modal that is not open does nothing', () => {
  fresh()
  const r = recording()
  closeModal('demo')
  expect(r.closed).toEqual([])
})

test('the default host opens a pane with focus and Escape, and never holdToasts', async () => {
  fresh()
  setModalHost()
  const calls: Array<Record<string, unknown>> = []
  setPaneApi({
    open: a => void calls.push(a),
    close: a => void calls.push({ closed: a.id }),
    invalidate: () => undefined,
  })
  openModal(spec, () => undefined)
  await settle()
  expect(calls[0]).toEqual({ id: 'pgd-demo', title: 'Demo', focus: true, closeOnEscape: true })
  expect('holdToasts' in calls[0]).toBe(false)
  closeModal('demo')
  expect(calls[1]).toEqual({ closed: 'pgd-demo' })
})

test('drawModal draws one row per item and one button per action, Close included', () => {
  fresh()
  const r = recording()
  setModalHost()
  setPaneApi({ open: () => undefined, close: () => undefined, invalidate: () => undefined })
  openModal(spec, () => undefined)
  void r
  const el = (type: string) => (p: Record<string, unknown>) => ({ type, ...p })
  const tree = drawModal({ Box: el('Box'), Text: el('Text'), Button: el('Button') }, 'demo') as any
  const rows = tree.children.slice(0, 2)
  expect(rows.map((x: any) => x.children.map((c: any) => c.children))).toEqual([
    ['one  ', '1'],
    ['two  ', '* 2'],
  ])
  expect(rows[1].children[1].color).toBe('red')
  const buttons = tree.children[2].children.filter((c: any) => c.type === 'Button')
  expect(buttons.map((b: any) => b.label)).toEqual(['Apply', 'Close'])
  pressAction('demo', CLOSE_ACTION)
  expect(isOpen('demo')).toBe(false)
})

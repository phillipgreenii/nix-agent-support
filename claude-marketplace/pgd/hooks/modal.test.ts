// Tests of the modal framework: openModal and closeModal through the injectable host, the absence
// of an added Close action (the engine's x closes the pane), the pane calls of the default host (no holdToasts) and the drawing.
import { expect, test } from 'claude-code/testing'
import { closeModal, drawModal, openModal, pressAction, resetModals, setModalHost, setPaneApi, type ModalSpec } from './modal'
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

test('openModal hands the host the spec as it is, with no Close action added', () => {
  fresh()
  const r = recording()
  openModal(spec, () => undefined)
  expect(r.opened).toHaveLength(1)
  expect(r.opened[0].actions.map(a => a.id)).toEqual(['apply'])
  expect(r.opened[0].items).toEqual(spec.items)
  expect(isOpen('demo')).toBe(true)
})

test('a spec with no actions produces no Close item', () => {
  fresh()
  const r = recording()
  openModal({ ...spec, actions: [] }, () => undefined)
  expect(r.opened[0].actions).toEqual([])
})

test('the actions a spec lists reach onAction; the pane stays open until the host or closeModal closes it', async () => {
  fresh()
  const r = recording()
  const seen: string[] = []
  openModal(spec, a => void seen.push(a))
  await r.handlers[0]('apply')
  expect(seen).toEqual(['apply'])
  expect(r.closed).toEqual([])
  expect(isOpen('demo')).toBe(true)
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

test('drawModal draws one row per item and one button per action, none for Close', () => {
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
  expect(buttons.map((b: any) => b.label)).toEqual(['Apply'])
  // a spec with no actions draws no actions row at all
  openModal({ ...spec, id: 'bare', actions: [] }, () => undefined)
  const bare = drawModal({ Box: el('Box'), Text: el('Text'), Button: el('Button') }, 'bare') as any
  expect(bare.children.some((c: any) => c.key === 'actions')).toBe(false)
  pressAction('demo', 'apply')
})

import { setVisible } from './source'
import { isOpen, onToggle, setOpen, syncOpen } from './toggles'

// The modal pane framework: a modal is a plain data description (ModalSpec), drawn as a pane by
// the ui.render hook below. The framework never uses holdToasts. A Close action is always added
// (id CLOSE_ACTION) and is handled here: it closes the modal and onAction never sees it; every
// other action id is passed to onAction.
export interface ModalAction {
  id: string
  label: string
}

export interface ModalItem {
  label: string
  value: string
  color?: string
  bold?: boolean
  dim?: boolean
  current?: boolean
  // A button drawn after the value of this row (the Focus pane's per-row open).
  action?: ModalAction
}

export interface ModalSpec {
  id: string
  title: string
  items: ModalItem[]
  actions: ModalAction[]
}

export type ActionHandler = (actionId: string) => void | Promise<void>

export const CLOSE_ACTION = 'close'

export interface ModalHost {
  open(spec: ModalSpec, onAction: ActionHandler): void
  close(id: string): void
}

// The real pane calls, reached through the register.ts facade (never `$` itself).
export interface PaneApi {
  open(args: { id: string; title: string; focus?: boolean; closeOnEscape?: true }): unknown
  close(args: { id: string }): unknown
  invalidate(): void
}

const live = new Map<string, { spec: ModalSpec; onAction: ActionHandler }>()
const builders = new Map<string, () => ModalSpec | undefined>()
let paneApi: PaneApi | undefined

export const paneId = (id: string): string => `pgd-${id}`

export function setPaneApi(p: PaneApi | undefined): void {
  paneApi = p
}

// Never rejects: a pane that cannot open is only logged by the engine.
const quiet = (r: unknown): void => {
  void Promise.resolve(r).catch(() => undefined)
}

const defaultHost: ModalHost = {
  open(spec, onAction) {
    live.set(spec.id, { spec, onAction })
    quiet(paneApi?.open({ id: paneId(spec.id), title: spec.title, focus: true, closeOnEscape: true }))
    paneApi?.invalidate()
  },
  close(id) {
    live.delete(id)
    quiet(paneApi?.close({ id: paneId(id) }))
  },
}

let modalHost: ModalHost = defaultHost

// Replace the host adapter (a test passes a recording host); no argument restores the default.
export function setModalHost(h?: ModalHost): void {
  modalHost = h ?? defaultHost
}

const openIds = new Set<string>()

// Whether source.ts has been told the band is visible. A draw tells it once (never on every
// draw: setVisible re-arms every source timer), and toggling the band tells it at once.
let bandShown = true

export function markBandDrawn(): void {
  if (bandShown) return
  bandShown = true
  setVisible('band', true)
}

export function openModal(spec: ModalSpec, onAction: ActionHandler): void {
  const actions = spec.actions.some(a => a.id === CLOSE_ACTION)
    ? spec.actions
    : [...spec.actions, { id: CLOSE_ACTION, label: 'Close' }]
  const full: ModalSpec = { ...spec, actions }
  openIds.add(spec.id)
  syncOpen(spec.id, true)
  setVisible('modal', true)
  modalHost.open(full, async actionId => {
    if (actionId === CLOSE_ACTION) closeModal(spec.id)
    else await onAction(actionId)
  })
}

export function closeModal(id: string): void {
  const was = openIds.delete(id)
  if (!was && !isOpen(id)) return
  syncOpen(id, false)
  if (openIds.size === 0) setVisible('modal', false)
  modalHost.close(id)
}

// The host closed the pane itself (Esc or dismiss): resync without closing it again.
export function hostClosed(id: string): void {
  const was = openIds.delete(id)
  live.delete(id)
  if (!was && !isOpen(id)) return
  syncOpen(id, false)
  if (openIds.size === 0) setVisible('modal', false)
}

// A modal's builder produces its spec from the cached values; it is called to open the modal and
// again on every draw, so the label of a button and its modal read the same source.
export function registerModalBuilder(
  id: string,
  build: () => ModalSpec | undefined,
  onAction?: ActionHandler,
  prepare?: () => Promise<void>,
): void {
  builders.set(id, build)
  if (prepare) preparers.set(id, prepare)
  else preparers.delete(id)
  actionHandlers.set(id, onAction ?? (() => undefined))
}

const actionHandlers = new Map<string, ActionHandler>()
const preparers = new Map<string, () => Promise<void>>()

export function openRegistered(id: string): void {
  const prepare = preparers.get(id)
  if (prepare) {
    // the modal opens once its one-time data is loaded (a microtask later when already loaded)
    void prepare().then(() => {
      if (isOpen(id) && !openIds.has(id)) openNow(id)
    })
    return
  }
  openNow(id)
}

function openNow(id: string): void {
  const spec = builders.get(id)?.()
  if (!spec) {
    // nothing to show yet: the toggle state is not left open
    syncOpen(id, false)
    return
  }
  openModal(spec, actionHandlers.get(id) ?? (() => undefined))
}

// The toggle listener: the toggle state of a modal id opens or closes it. The band's toggle is
// handled in band.ts's own listener branch (setVisible).
onToggle((id, open) => {
  if (id === 'band') {
    bandShown = open
    setVisible('band', open)
    return
  }
  if (!builders.has(id)) return
  if (open) openRegistered(id)
  else closeModal(id)
})

export function isModalOpen(id: string): boolean {
  return openIds.has(id)
}

// The spec a draw shows: the live builder's, else the one the modal was opened with.
export function currentSpec(id: string): ModalSpec | undefined {
  const built = builders.get(id)?.()
  const base = built ?? live.get(id)?.spec
  if (!base) return undefined
  return base.actions.some(a => a.id === CLOSE_ACTION)
    ? base
    : { ...base, actions: [...base.actions, { id: CLOSE_ACTION, label: 'Close' }] }
}

export function pressAction(id: string, actionId: string): void {
  if (actionId === CLOSE_ACTION) {
    closeModal(id)
    return
  }
  const h = live.get(id)?.onAction ?? actionHandlers.get(id)
  if (h) void Promise.resolve(h(actionId)).catch(() => undefined)
}

export function resetModals(): void {
  live.clear()
  builders.clear()
  actionHandlers.clear()
  preparers.clear()
  openIds.clear()
  modalHost = defaultHost
  paneApi = undefined
  bandShown = true
}

// Draw one modal pane as plain Box, Text and Button elements.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function drawModal(ui: any, id: string): unknown {
  const spec = currentSpec(id)
  if (!spec) return undefined
  const { Box, Text, Button } = ui
  const width = Math.max(0, ...spec.items.map(i => i.label.length))
  return Box({
    flexDirection: 'column',
    children: [
      ...spec.items.map((it, i) =>
        Box({
          key: `item-${i}`,
          flexDirection: 'row',
          children: [
            Text({ key: 'label', dimColor: true, children: `${it.label.padEnd(width)}  ` }),
            Text({
              key: 'value',
              ...(it.color ? { color: it.color } : {}),
              ...(it.bold ? { bold: true } : {}),
              ...(it.dim ? { dimColor: true } : {}),
              children: `${it.current ? '* ' : ''}${it.value}` }),
            ...(it.action
              ? [
                  Text({ key: 'row-gap', children: ' ' }),
                  Button({ key: 'row-action', label: it.action.label, onPress: () => pressAction(id, (it.action as ModalAction).id) }),
                ]
              : []),
          ],
        }),
      ),
      Box({
        key: 'actions',
        flexDirection: 'row',
        children: spec.actions.flatMap((a, i) => [
          ...(i > 0 ? [Text({ key: `gap-${a.id}`, children: ' ' })] : []),
          Button({ key: `action-${a.id}`, label: a.label, onPress: () => pressAction(id, a.id) }),
        ]),
      }),
    ],
  })
}

type Next = (e: unknown) => unknown

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function registerModalHooks(on: any): void {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  on('ui.render', { component: 'Pane' }, ($: any, e: any, next: Next) => {
    const rid = typeof e?.requestId === 'string' ? e.requestId : ''
    if (!rid.startsWith('pgd-')) return next(e)
    const id = rid.slice(4)
    const el = drawModal($.ui.resolve(e), id)
    return el === undefined ? next(e) : el
  })
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  on('ui.close', (_$: any, e: any, next: Next) => {
    if (e?.origin === 'person' && typeof e?.id === 'string' && e.id.startsWith('pgd-')) {
      hostClosed(e.id.slice(4))
    }
    return next(e)
  })
}

// setOpen/isOpen re-exported for callers that only import the framework.
export { isOpen, setOpen }

// After the toggle state is hydrated: a band hidden before a reload stays hidden.
export function applyBandState(): void {
  if (isOpen('band')) return
  bandShown = false
  setVisible('band', false)
}

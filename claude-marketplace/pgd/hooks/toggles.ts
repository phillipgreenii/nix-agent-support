// Toggle state of the band and each modal. The state is a plain map held here (so isOpen is
// synchronous inside a draw) and mirrored to a persistent store: register.ts binds the store to an
// engine atom (setToggleStore) and hydrates the map from it at session.start, which is how the
// state survives a hot reload of the plugin. The band is open (visible) until toggled off; a modal
// is closed until opened.
export type ToggleState = Record<string, boolean>

export interface ToggleStore {
  save(state: ToggleState): void
}

type Listener = (id: string, open: boolean) => void

const DEFAULT_OPEN: ToggleState = { band: true }

let state: ToggleState = {}
let store: ToggleStore | undefined
const listeners: Listener[] = []

export function isOpen(id: string): boolean {
  return id in state ? state[id] === true : DEFAULT_OPEN[id] === true
}

// Set a toggle, tell the one listener (which opens or closes the real thing) and persist.
export function setOpen(id: string, open: boolean): void {
  if (isOpen(id) === open && id in state) return
  state = { ...state, [id]: open }
  for (const l of [...listeners]) l(id, open)
  store?.save(state)
}

export function toggle(id: string): void {
  setOpen(id, !isOpen(id))
}

// Record a change that already happened (a modal opened or closed by its own code, or closed by
// the host): updates and persists the state without telling the listener.
export function syncOpen(id: string, open: boolean): void {
  if (isOpen(id) === open && id in state) return
  state = { ...state, [id]: open }
  store?.save(state)
}

// Subscribe to toggle changes made through setOpen or toggle; returns the unsubscribe function.
export function onToggle(l: Listener): () => void {
  listeners.push(l)
  return () => {
    const i = listeners.indexOf(l)
    if (i >= 0) listeners.splice(i, 1)
  }
}

export function setToggleStore(s: ToggleStore | undefined): void {
  store = s
}

// Replace the state with a persisted one (a malformed value is ignored); never persists.
export function hydrateToggles(saved: unknown): void {
  if (!saved || typeof saved !== 'object' || Array.isArray(saved)) return
  const next: ToggleState = {}
  for (const [k, v] of Object.entries(saved as Record<string, unknown>)) {
    if (typeof v === 'boolean') next[k] = v
  }
  state = next
}

export function toggleSnapshot(): ToggleState {
  return { ...state }
}

export function resetToggles(): void {
  state = {}
  store = undefined
}

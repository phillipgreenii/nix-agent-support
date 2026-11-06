// Tests of the toggle state: the defaults, setOpen and toggle, persistence to the store, and the
// survival of a hot reload (the module's own state starts over, the persisted state is hydrated).
import { expect, test } from 'claude-code/testing'
import { hydrateToggles, isOpen, onToggle, resetToggles, setOpen, setToggleStore, syncOpen, toggle, toggleSnapshot } from './toggles'

test('the band is open and every modal closed until toggled', () => {
  resetToggles()
  expect(isOpen('band')).toBe(true)
  expect(isOpen('info')).toBe(false)
  expect(isOpen('context')).toBe(false)
})

test('setOpen and toggle change the state, tell the listener once per change and persist', () => {
  resetToggles()
  const told: string[] = []
  const saved: Array<Record<string, boolean>> = []
  const off = onToggle((id, open) => void told.push(`${id}=${open}`))
  setToggleStore({ save: s => void saved.push(s) })
  toggle('info')
  expect(isOpen('info')).toBe(true)
  setOpen('info', true)
  toggle('band')
  expect(isOpen('band')).toBe(false)
  toggle('info')
  expect(told).toEqual(['info=true', 'band=false', 'info=false'])
  expect(saved[saved.length - 1]).toEqual({ info: false, band: false })
  off()
})

test('syncOpen records a change without telling the listener', () => {
  resetToggles()
  const told: string[] = []
  const off = onToggle((id, open) => void told.push(`${id}=${open}`))
  syncOpen('context', true)
  expect(isOpen('context')).toBe(true)
  expect(told).toEqual([])
  off()
})

test('toggle state survives a hot reload: a reloaded module hydrates what was persisted', () => {
  resetToggles()
  let persisted: Record<string, boolean> = {}
  setToggleStore({ save: s => void (persisted = s) })
  setOpen('band', false)
  setOpen('context', true)
  // the reload: the module's own variables start over, the atom (here persisted) stays
  resetToggles()
  expect(isOpen('band')).toBe(true)
  hydrateToggles(persisted)
  expect(isOpen('band')).toBe(false)
  expect(isOpen('context')).toBe(true)
  expect(isOpen('info')).toBe(false)
  expect(toggleSnapshot()).toEqual({ band: false, context: true })
})

test('a malformed persisted value is ignored', () => {
  resetToggles()
  hydrateToggles('nope')
  hydrateToggles([true])
  hydrateToggles({ band: 'x', info: true })
  expect(isOpen('band')).toBe(true)
  expect(isOpen('info')).toBe(true)
})

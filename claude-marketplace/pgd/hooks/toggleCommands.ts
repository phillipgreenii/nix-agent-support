import { getOptions } from './options'
import { isOpen, toggle } from './toggles'
import type { ModApi } from './types'

// One slash command per toggle: /pgd-band, /pgd-info and /pgd-context, no arguments. Each toggles
// its target and answers with the new state, so the band and each modal can be reached from the
// keyboard when the band is hidden (the band's own button is then not drawn).
export const TOGGLE_COMMANDS: ReadonlyArray<{ name: string; id: string; description: string }> = [
  { name: 'pgd-band', id: 'band', description: 'Show or hide the pgd band above the prompt' },
  { name: 'pgd-info', id: 'info', description: 'Show or hide the pgd Info pane' },
  { name: 'pgd-context', id: 'context', description: 'Show or hide the pgd Context and usage pane' },
]

export function registerToggleCommandSpecs(host: ModApi): void {
  for (const c of TOGGLE_COMMANDS) void host.command.register({ name: c.name, description: c.description })
}

export function registerToggleCommands(on: ModApi): void {
  for (const c of TOGGLE_COMMANDS) {
    on('command.run', { command: c.name }, () => {
      if (!getOptions()?.enable) return { text: 'pgd is disabled.' }
      toggle(c.id)
      return { text: `${c.id} ${isOpen(c.id) ? 'shown' : 'hidden'}.` }
    })
  }
}

// The Focus pane's toggle command, /pgd-focus. Kept apart from TOGGLE_COMMANDS because its
// command spec is registered only while components.focus is on (by focus-pane.ts); the run hook
// is registered at load and answers that the pane is disabled when the flag is off.
export function registerFocusToggleCommand(on: ModApi): void {
  on('command.run', { command: 'pgd-focus' }, () => {
    const o = getOptions()
    if (!o?.enable || !o.components.focus) return { text: 'pgd focus is disabled.' }
    toggle('focus')
    return { text: `focus ${isOpen('focus') ? 'shown' : 'hidden'}.` }
  })
}

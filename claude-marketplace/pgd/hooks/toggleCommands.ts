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

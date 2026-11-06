import { registerBandButton, registerDirectAction } from './band'
import type { PgdOptions, ModApi } from './types'

// The Wrap up band button (band order 70, after Focus). Always drawn while enable is true; it has
// no component flag. Pressing it opens no modal: it pre-fills the wrap-up skill's plugin-qualified
// slash command in the prompt box with host.prompt.fill and never submits (a submit from a
// command does not start a turn until the next user input).
//  - Command text: the skill is wrap-up-session in the session-wrapup plugin, so the command is
//    /session-wrapup:wrap-up-session.
//  - Glyph: none; the label is the plain text "Wrap up".
export const WRAP_UP_COMMAND = '/session-wrapup:wrap-up-session'
export const WRAP_UP_LABEL = 'Wrap up'
export const WRAP_UP_ACTION = 'wrapup'
export const WRAP_UP_ORDER = 70

export function startWrapUp(_options: PgdOptions, host: ModApi): void {
  registerBandButton({ id: 'wrapup', order: WRAP_UP_ORDER, label: () => WRAP_UP_LABEL, action: WRAP_UP_ACTION })
  registerDirectAction(WRAP_UP_ACTION, () => {
    try {
      void Promise.resolve(host.prompt.fill({ text: WRAP_UP_COMMAND, mode: 'replace' })).catch(() => undefined)
    } catch {
      // a failed fill leaves the prompt box as it is
    }
  })
}

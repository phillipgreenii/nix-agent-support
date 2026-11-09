import type { PgdOptions, ModApi } from './types'
import { startDolt } from './dolt'
import { registerModals } from './modals'
import { startPr } from './pr'
import { startSessionMode } from './session-mode'
import { startModelModal } from './model-modal'
import { startWrapUp } from './wrap-up'
import { startFocusPane } from './focus-pane'
import { startSessionName } from './session-name'
import { startAssume } from './assume'

// FINAL registration shape (checked by `claude plugin validate`, which refuses a module that
// passes `$` itself as an argument):
//
//   registerComponents(options: PgdOptions, host: ModApi): void
//
// `host` is NOT `$`: register.ts builds it as a facade object of closures, each member a
// `$.noun.method(...)` call site (process.run, fs.read/write, http.fetch, config.set).
// A component is (options, host) => void and touches host only when its flag is true, so a
// component that is off registers nothing: no button, modal, source, timer, alert, command or
// $.env.set name. Each later component adds its entry to COMPONENTS and, if it needs another `$`
// member, one closure to the facade in register.ts; nothing ever passes `$` as an argument.
export type Component = (options: PgdOptions, host: ModApi) => void

export const COMPONENTS: ReadonlyArray<{
  flag: keyof PgdOptions['components']
  run: Component
}> = [
  { flag: 'beads', run: startDolt },
  { flag: 'prStatus', run: startPr },
  { flag: 'sessionMode', run: startSessionMode },
  { flag: 'modelEffort', run: startModelModal },
  { flag: 'focus', run: startFocusPane },
  { flag: 'sessionName', run: startSessionName },
  { flag: 'assume', run: startAssume },
]

// Register every component whose flag is true. The top-level `enable` short-circuits all of it.
export function registerComponents(options: PgdOptions, host: ModApi): void {
  if (!options.enable) return
  for (const c of COMPONENTS) {
    if (options.components[c.flag]) c.run(options, host)
  }
  // The band buttons' modals (Info, Context and usage) and their toggle commands: no flag of
  // their own, governed by enable.
  registerModals(options, host)
  // The Wrap up button: no flag of its own, always drawn while enable is true.
  startWrapUp(options, host)
}

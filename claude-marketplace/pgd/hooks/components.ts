import type { PgdOptions, ModApi } from './types'
import { startDolt } from './dolt'
import { registerPgdNoun } from './pgd-noun'
import { registerModals } from './modals'

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
}> = [{ flag: 'beads', run: startDolt }]

// Register every component whose flag is true. The top-level `enable` short-circuits all of it.
export function registerComponents(options: PgdOptions, host: ModApi): void {
  if (!options.enable) return
  for (const c of COMPONENTS) {
    if (options.components[c.flag]) c.run(options, host)
  }
  // The pgd noun is registered whenever enable is true; its focus surface is gated by
  // components.focus inside the noun.
  registerPgdNoun(options, host)
  // The band buttons' modals (Info, Context and usage) and their toggle commands: no flag of
  // their own, governed by enable.
  registerModals(options, host)
}

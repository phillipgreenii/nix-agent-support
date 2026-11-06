import type { DisplayOptions, ModApi } from './types'

// A component is a function (options, $) => void that touches $ only when its flag is true, so a
// component that is off registers nothing: no button, modal, source, timer, alert, command or
// $.env.set name. Each later component adds its entry to COMPONENTS and nothing else.
export type Component = (options: DisplayOptions, $: ModApi) => void

export const COMPONENTS: ReadonlyArray<{
  flag: keyof DisplayOptions['components']
  run: Component
}> = []

// Register every component whose flag is true. The top-level `enable` short-circuits all of it.
export function registerComponents(options: DisplayOptions, $: ModApi): void {
  if (!options.enable) return
  for (const c of COMPONENTS) {
    if (options.components[c.flag]) c.run(options, $)
  }
}

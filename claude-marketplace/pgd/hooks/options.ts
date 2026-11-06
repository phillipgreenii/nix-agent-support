import type { PgdOptions } from './types'

// The parsed options, shared with the hooks that are registered at registration time through
// `on`. Those hooks cannot see the options register.ts parses inside session.start, so
// register.ts calls setOptions there (before initSources) and every registration-time hook reads
// getOptions(): it returns next(e) untouched when the result is undefined or enable is false.
let current: PgdOptions | undefined

export function setOptions(options: PgdOptions): void {
  current = options
}

export function getOptions(): PgdOptions | undefined {
  return current
}

// True when the display is on: options were set and the top-level enable is true.
export function pgdEnabled(): boolean {
  return current?.enable === true
}

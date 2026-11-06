import { defineSource } from './source'
import type { ModApi, PgdOptions } from './types'

// The dolt availability probe: `bd dolt test` (a connection test of the beads server) with a
// five-second timeout. A zero exit is available; a non-zero exit, a refused connection or a
// timeout (the run rejects) is unavailable. Polled at most every 30 seconds, always (it feeds the
// alert row, which survives a collapsed band), shared across sessions through the plugin store, with
// backoff from the source layer. It is defined only by the beads component, so with the flag off
// there is no probe, no timer and no alert.
export const DOLT_INTERVAL_MS = 30_000
export const DOLT_TIMEOUT_MS = 5_000

export async function probeDolt(host: ModApi): Promise<boolean> {
  try {
    const r = await host.process.run(['bd', 'dolt', 'test'], { timeoutMs: DOLT_TIMEOUT_MS })
    return r?.exitCode === 0
  } catch {
    return false
  }
}

export function startDolt(_options: PgdOptions, _host: ModApi): void {
  defineSource<boolean>({
    id: 'dolt',
    intervalMs: DOLT_INTERVAL_MS,
    shared: true,
    feeds: ['alert'],
    fetch: ({ host }) => probeDolt(host),
  })
}

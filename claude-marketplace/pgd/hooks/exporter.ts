import { getOptions } from './options'
import { snapshot } from './metrics'
import { log } from './log'
import type { ModApi } from './types'

// The one exporter module. exportMetrics() is a no-op unless PgdOptions.metrics.endpoint is set:
// it returns before touching the host or the metrics. With an endpoint it posts the snapshot as
// JSON through host.http.fetch, with one export in flight, and never throws; register.ts calls it
// from a timer without awaiting it, so it cannot block a draw. Whether an OTLP wire format is
// accepted under the machine's web policy is a separate spike; the payload here is the plain
// snapshot (the wire details are the builder's choice).
let host: ModApi
let inFlight = false

export function initExporter(h: ModApi): void {
  host = h
  inFlight = false
}

export async function exportMetrics(): Promise<void> {
  const endpoint = getOptions()?.metrics?.endpoint
  if (!endpoint || !host || inFlight) return
  inFlight = true
  try {
    await host.http.fetch(endpoint, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ metrics: snapshot() }),
    })
  } catch {
    log('warn', 'exporter', 'metrics export failed', 'other')
  } finally {
    inFlight = false
  }
}

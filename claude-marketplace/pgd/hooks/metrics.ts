// In-memory counters and histograms of the display (observability, phase 1). record() is a plain
// map update: it never touches the host, so a draw can call it without starting a fetch, a process
// or a file read. Choices made by the builder (the design leaves them open):
//  - A name ending in _seconds is a histogram, kept as a running sum and count (the bucket layout
//    is the exporter's); every other name is a counter that adds `value`.
//  - snapshot() returns one entry per series; a histogram yields <name>_sum and <name>_count.
//  - Durations are in seconds. The draw timer reads performance.now() when present.
export type Labels = Record<string, string>
export interface Sample {
  name: string
  labels: Labels
  value: number
}

export const METRIC_NAMES = [
  'pgd_fetch_total',
  'pgd_fetch_errors_total',
  'pgd_fetch_duration_seconds',
  'pgd_cache_hits_total',
  'pgd_cache_misses_total',
  'pgd_draw_total',
  'pgd_draw_duration_seconds',
  'pgd_invalidate_total',
  'pgd_invalidate_coalesced_total',
  'pgd_links_resolved_total',
  'pgd_links_declined_total',
] as const

const series = new Map<string, Sample>()

function key(name: string, labels: Labels): string {
  const sorted = Object.keys(labels)
    .sort()
    .map(k => [k, labels[k]])
  return `${name}${JSON.stringify(sorted)}`
}

function add(name: string, labels: Labels, value: number): void {
  const k = key(name, labels)
  const s = series.get(k)
  if (s) s.value += value
  else series.set(k, { name, labels: { ...labels }, value })
}

export function record(name: string, value: number, labels: Labels = {}): void {
  if (name.endsWith('_seconds')) {
    add(`${name}_sum`, labels, value)
    add(`${name}_count`, labels, 1)
  } else {
    add(name, labels, value)
  }
}

export function snapshot(): Sample[] {
  return [...series.values()].map(s => ({ name: s.name, labels: { ...s.labels }, value: s.value }))
}

export function resetMetrics(): void {
  series.clear()
}

let nowSeconds = (): number => {
  const p = (globalThis as { performance?: { now?: () => number } }).performance
  return (p?.now ? p.now() : Date.now()) / 1000
}

// Test hook: replace the draw timer's clock (seconds).
export function setMetricsClock(fn: () => number): void {
  nowSeconds = fn
}
export function metricsNow(): number {
  return nowSeconds()
}

// Time one draw: records pgd_draw_total and pgd_draw_duration_seconds for the surface, once the
// draw (sync or async) has finished, and returns fn's result untouched. A throw is still recorded.
export function timedDraw<T>(surface: string, fn: () => T): T {
  const start = nowSeconds()
  const done = (): void => {
    record('pgd_draw_total', 1, { surface })
    record('pgd_draw_duration_seconds', nowSeconds() - start, { surface })
  }
  let out: T
  try {
    out = fn()
  } catch (err) {
    done()
    throw err
  }
  if (out && typeof (out as { then?: unknown }).then === 'function') {
    return (out as unknown as Promise<unknown>).then(
      v => {
        done()
        return v
      },
      err => {
        done()
        throw err
      },
    ) as T
  }
  done()
  return out
}

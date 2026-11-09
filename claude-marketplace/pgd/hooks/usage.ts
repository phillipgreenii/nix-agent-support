import { captureLimits } from './limits-capture'
import { pgdEnabled } from './options'
import { defineSource, noteEvent, setValue } from './source'
import type { ModApi } from './types'

// The usage source (context, limits, cost): event-driven (intervalMs 0), updated from the
// session.measure hook, which then reads $.session.usage() for the figures. Field mapping
// (engine -> value): context.percent -> contextPercent, context.tokens -> contextTokens,
// rateLimits[] of kind five_hour or seven_day -> limits[] { kind, usedPercent: percentUsed,
// resetsAt }, cost.usd -> costUsd. Other limit kinds (a gateway spend limit) are not drawn.
export interface Limit {
  kind: 'five_hour' | 'seven_day'
  usedPercent: number
  resetsAt?: string
}

export interface Usage {
  contextPercent?: number
  contextTokens?: number
  limits: Limit[]
  costUsd?: number
}

interface RawUsage {
  context?: { percent?: number; tokens?: number }
  rateLimits?: Array<{ kind: string; percentUsed: number; resetsAt?: string }>
  cost?: { usd?: number }
}

export function mapUsage(u: RawUsage): Usage {
  const out: Usage = { limits: [] }
  if (typeof u.context?.percent === 'number') out.contextPercent = u.context.percent
  if (typeof u.context?.tokens === 'number') out.contextTokens = u.context.tokens
  for (const r of u.rateLimits ?? []) {
    if (r.kind !== 'five_hour' && r.kind !== 'seven_day') continue
    const l: Limit = { kind: r.kind, usedPercent: r.percentUsed }
    if (r.resetsAt !== undefined) l.resetsAt = r.resetsAt
    out.limits.push(l)
  }
  if (typeof u.cost?.usd === 'number') out.costUsd = u.cost.usd
  return out
}

export function startUsage(_host: ModApi): void {
  defineSource<Usage>({ id: 'usage', intervalMs: 0, feeds: ['band', 'modal', 'alert'] })
}

export function registerUsageHooks(on: ModApi): void {
  on('session.measure', async ($: ModApi, e: unknown, next: (e: unknown) => unknown) => {
    if (pgdEnabled()) {
      noteEvent()
      try {
        const usage = mapUsage(await $.session.usage())
        setValue('usage', usage)
        // The limit readings also go to the session's status.jsonl, the file pa-monitor reads.
        captureLimits(usage)
      } catch {
        // keep the last cached value; the next measurement tries again
      }
    }
    return next(e)
  })
}

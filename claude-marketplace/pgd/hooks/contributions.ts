// The store of live contributions pushed through the pgd noun. Internal storage is a map keyed by
// id; everything it needs from the world (the clock, a timer and the redraw request) is injected
// by initContributions, so a test drives it with a fake clock.
import type { ContributionSurface, PutArgs } from '../types/pgd'

export interface ContributionDeps {
  now: () => number
  setTimer: (fn: () => void, delayMs: number) => { cancel(): void }
  requestInvalidate: () => void
}

interface Stored {
  args: PutArgs
  seq: number
  expiresAt?: number
}

const store = new Map<string, Stored>()
let seq = 0
let deps: ContributionDeps = {
  now: () => Date.now(),
  setTimer: fn => {
    void fn
    return { cancel: () => undefined }
  },
  requestInvalidate: () => undefined,
}
let timer: { cancel(): void } | undefined

export function initContributions(d: ContributionDeps): void {
  deps = d
  timer?.cancel()
  timer = undefined
  store.clear()
  seq = 0
}

// Drop what has expired; true when something was dropped.
function prune(): boolean {
  const t = deps.now()
  let dropped = false
  for (const [id, s] of store) {
    if (s.expiresAt !== undefined && s.expiresAt <= t) {
      store.delete(id)
      dropped = true
    }
  }
  return dropped
}

// One timer, set for the earliest expiry; the display redraws when an item disappears.
function schedule(): void {
  timer?.cancel()
  timer = undefined
  let earliest: number | undefined
  for (const s of store.values()) {
    if (s.expiresAt !== undefined && (earliest === undefined || s.expiresAt < earliest)) earliest = s.expiresAt
  }
  if (earliest === undefined) return
  timer = deps.setTimer(
    () => {
      timer = undefined
      if (prune()) deps.requestInvalidate()
      schedule()
    },
    Math.max(0, earliest - deps.now()),
  )
}

export function putContribution(args: PutArgs): void {
  const expiresAt = typeof args.ttlMs === 'number' ? deps.now() + args.ttlMs : undefined
  store.set(args.id, { args: { ...args }, seq: seq++, expiresAt })
  schedule()
  deps.requestInvalidate()
}

export function removeContribution(id: string): void {
  store.delete(id)
  schedule()
  deps.requestInvalidate()
}

// The live contributions of a surface: larger priority first, then the one added earlier first.
// Expiry is applied here, so a read never returns an expired item.
export function listContributions(surface: ContributionSurface): PutArgs[] {
  prune()
  return [...store.values()]
    .filter(s => s.args.surface === surface)
    .sort((a, b) => b.args.priority - a.args.priority || a.seq - b.seq)
    .map(s => ({ ...s.args }))
}

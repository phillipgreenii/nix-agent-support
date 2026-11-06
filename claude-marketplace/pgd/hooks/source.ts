// The source layer of the display: a Cache-Aside entry per source, so a draw reads cached values
// only (readCached) and never starts a fetch, a process or a file read.
//
// Each source owns its interval, one in-flight fetch, a failure backoff and a cached
// { value, fetchedAt }. Choices made by the builder (the design leaves them open):
//  - Host handle: `$` is never passed. fetch receives { host }, the facade register.ts builds
//    inside the session.start hook (an object of closures, each a `$.noun.method(...)` call
//    site). The members the source layer itself uses: host.ui.invalidate(), host.clock.after(ms,
//    fn) (through the setTimer injected by register.ts) and whatever a fetch reads.
//  - Clock: initSources(host, { now, setTimer }); now defaults to Date.now, setTimer to
//    setTimeout; tests inject a fake clock and call initSources again, which resets the registry.
//  - intervalMs 0: an event-driven source, never polled; setValue(id, value) updates it, and an
//    optional fetch is run only by refresh(id) (no throttle beyond one in flight).
//  - Polled source: fetched once at start (when active), then every intervalMs * idleFactor. After
//    a failed fetch the delay is intervalMs * idleFactor * 2^failures (failures >= 1), so it is
//    strictly greater than intervalMs, at most one attempt is made per delay, and it is capped at
//    ten minutes (never below the interval). A fetch resolving undefined is a value (absent), not a
//    failure; only a rejection counts as a failure.
//  - Visibility: Surface is 'band' | 'modal' | 'alert'; initially band and alert are visible and
//    modal is not. A source runs a timer only while a surface in its feeds is visible; a source
//    feeding 'alert' always runs; a source with no feeds never runs a timer.
//  - Idle: after five minutes without noteEvent() every polled interval is multiplied by four
//    until the next event. A timer that fires early under the idle factor re-arms for the rest.
//  - Changed means JSON-equal comparison. requestInvalidate() coalesces to the end of the current
//    microtask turn and then calls host.ui.invalidate() once; setValue and a fetch call it only
//    when the cached value changed.
//  - refresh(id): an immediate fetch for an active polled source, skipped while one is in flight
//    or when the last attempt is younger than the current delay (the timer then covers it).
//  - shared is accepted and stored but inert here.
import type { ModApi } from './types'

export type Surface = 'band' | 'modal' | 'alert'

export interface SourceDef<T> {
  id: string
  intervalMs: number
  fetch?: (ctx: { host: ModApi }) => Promise<T | undefined>
  shared?: boolean
  feeds?: Surface[]
}

export interface Clock {
  now: () => number
  setTimer: (fn: () => void, delayMs: number) => { cancel(): void }
}

const IDLE_AFTER_MS = 5 * 60 * 1000
const IDLE_FACTOR = 4
const MAX_BACKOFF_MS = 10 * 60 * 1000

interface Entry {
  def: SourceDef<unknown>
  cache?: { value: unknown; fetchedAt: number }
  inFlight: boolean
  failures: number
  lastAttemptAt?: number
  timer?: { cancel(): void }
}

const defaultClock: Clock = {
  now: () => Date.now(),
  setTimer: (fn, ms) => {
    const t = setTimeout(fn, ms)
    return { cancel: () => clearTimeout(t) }
  },
}

let host: ModApi
let clock: Clock = defaultClock
let entries = new Map<string, Entry>()
let visible: Record<Surface, boolean> = { band: true, modal: false, alert: true }
let lastEventAt = 0
let invalidatePending = false

const same = (a: unknown, b: unknown): boolean =>
  (JSON.stringify(a) ?? 'undefined') === (JSON.stringify(b) ?? 'undefined')

const isIdle = (): boolean => clock.now() - lastEventAt >= IDLE_AFTER_MS

function active(e: Entry): boolean {
  const d = e.def
  if (d.intervalMs <= 0) return false
  const feeds = d.feeds ?? []
  if (feeds.includes('alert')) return true
  return feeds.some(s => visible[s])
}

// The delay between two attempts: the interval (times the idle factor), doubled per failure.
function delayOf(e: Entry): number {
  const base = e.def.intervalMs * (isIdle() ? IDLE_FACTOR : 1)
  if (e.failures === 0) return base
  return Math.max(base * 2, Math.min(base * 2 ** e.failures, MAX_BACKOFF_MS))
}

function schedule(e: Entry): void {
  e.timer?.cancel()
  e.timer = undefined
  if (!host || !active(e)) return
  const wait =
    e.lastAttemptAt === undefined ? 0 : Math.max(0, e.lastAttemptAt + delayOf(e) - clock.now())
  e.timer = clock.setTimer(() => {
    e.timer = undefined
    if (e.inFlight) return // the fetch's completion re-arms the timer
    if (!active(e)) return
    if (e.lastAttemptAt !== undefined && clock.now() < e.lastAttemptAt + delayOf(e)) {
      schedule(e)
      return
    }
    void runFetch(e)
  }, wait)
}

async function runFetch(e: Entry): Promise<void> {
  if (e.inFlight || !e.def.fetch) return
  e.inFlight = true
  e.lastAttemptAt = clock.now()
  try {
    const value = await e.def.fetch({ host })
    e.failures = 0
    store(e, value)
  } catch {
    e.failures += 1
  } finally {
    e.inFlight = false
    schedule(e)
  }
}

function store(e: Entry, value: unknown): void {
  const changed = !same(e.cache?.value, value)
  e.cache = { value, fetchedAt: clock.now() }
  if (changed) requestInvalidate()
}

// Reset the registry, keep the host facade and the clock. Called once from inside the
// session.start hook (a reload runs it again, so no source or timer is duplicated).
export function initSources(h: ModApi, c?: Partial<Clock>): void {
  for (const e of entries.values()) e.timer?.cancel()
  entries = new Map()
  host = h
  clock = { ...defaultClock, ...c }
  visible = { band: true, modal: false, alert: true }
  lastEventAt = clock.now()
  invalidatePending = false
}

export function defineSource<T>(def: SourceDef<T>): void {
  entries.get(def.id)?.timer?.cancel()
  const e: Entry = { def: def as SourceDef<unknown>, inFlight: false, failures: 0 }
  entries.set(def.id, e)
  schedule(e)
}

// The last cached value, or undefined (blank, missing and not-yet-fetched alike). Never fetches.
export function readCached<T>(id: string): T | undefined {
  return entries.get(id)?.cache?.value as T | undefined
}

// Update an event-driven source. A source that was never started makes this a no-op.
export function setValue(id: string, value: unknown): void {
  const e = entries.get(id)
  if (e) store(e, value)
}

// Coalesce any number of changes in one event turn into one host.ui.invalidate() call.
export function requestInvalidate(): void {
  if (invalidatePending || !host) return
  invalidatePending = true
  void Promise.resolve().then(() => {
    invalidatePending = false
    host?.ui.invalidate()
  })
}

export function refresh(id: string): void {
  const e = entries.get(id)
  if (!e || e.inFlight || !e.def.fetch) return
  if (e.def.intervalMs > 0) {
    if (!active(e)) return
    if (e.lastAttemptAt !== undefined && clock.now() < e.lastAttemptAt + delayOf(e)) return
  }
  void runFetch(e)
}

// An engine event happened: ends idleness (re-arming every timer at the normal interval).
export function noteEvent(): void {
  const wasIdle = isIdle()
  lastEventAt = clock.now()
  if (wasIdle) for (const e of entries.values()) schedule(e)
}

export function setVisible(surface: Surface, isVisible: boolean): void {
  visible[surface] = isVisible
  for (const e of entries.values()) schedule(e)
}

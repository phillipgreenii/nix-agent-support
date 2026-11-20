// Tests of the /clear and /resume refresh: session.end with reason 'clear' or 'resume' drops the
// conversation's cached values at once, waits for the new session id, then reads them again; any
// other end reason changes nothing.
import { expect, test } from 'claude-code/testing'
import { CLEAR_RETRY_DELAYS_MS, registerClearHooks } from './clear'
import { readCompactionOnce } from './compaction'
import { loadSessionFacts, resetSessionFacts } from './info-modal'
import { captureLimits, flushLimitsCapture, initLimitsCapture } from './limits-capture'
import { setOptions } from './options'
import { defineSource, initSources, readCached, setValue } from './source'
import { DEFAULT_OPTIONS } from './types'
import { fakeClock, settle, stubHost } from './testkit'

type Hook = (...a: unknown[]) => Promise<unknown>

function setup() {
  const stub = stubHost()
  initSources(stub.host, fakeClock())
  setOptions(DEFAULT_OPTIONS)
  const fetches: Record<string, number> = {}
  const counted = (id: string) => async () => {
    fetches[id] = (fetches[id] ?? 0) + 1
    return `${id}-new`
  }
  // Fetchable sources count their reads; event-driven ones only hold a value.
  for (const id of ['sessionName', 'sessionMode', 'compactionMarker', 'gitHead']) {
    defineSource<string>({ id, intervalMs: 0, fetch: counted(id) })
  }
  defineSource({ id: 'usage', intervalMs: 0 })
  defineSource({ id: 'lastCompaction', intervalMs: 0 })
  for (const id of ['usage', 'sessionName', 'sessionMode', 'compactionMarker', 'lastCompaction', 'gitHead']) {
    setValue(id, id === 'usage' ? { contextPercent: 62, limits: [] } : `${id}-old`)
  }
  let hook: Hook | undefined
  registerClearHooks(((name: string, fn: Hook) => {
    if (name === 'session.end') hook = fn
  }) as never)
  const timers: Array<{ ms: number; fn: () => unknown }> = []
  const world = { id: 'old-id', usageError: false, usagePercent: 0 }
  const $ = {
    clock: { after: (ms: number, fn: () => unknown) => void timers.push({ ms, fn }) },
    session: {
      id: async () => world.id,
      usage: async () => {
        if (world.usageError) throw new Error('usage down')
        return { context: { percent: world.usagePercent, tokens: 0 }, rateLimits: [] }
      },
    },
  }
  // Run the oldest pending timer (the hook schedules the next one from inside a timer).
  const fire = async (): Promise<number | undefined> => {
    const t = timers.shift()
    if (!t) return undefined
    await t.fn()
    await settle()
    return t.ms
  }
  return { hook: hook!, $, timers, fire, world, fetches, stub }
}

const END = { reason: 'clear', sessionId: 'old-id' }

test('a clear drops every conversation-scoped value at once, before any timer runs', async () => {
  const { hook, $, fetches } = setup()
  await hook($, END, async () => ({}))
  for (const id of ['usage', 'sessionName', 'sessionMode', 'compactionMarker', 'lastCompaction']) {
    expect(readCached(id)).toBe(undefined)
  }
  expect(readCached('gitHead')).toBe('gitHead-old') // the branch does not change with a conversation
  expect(fetches).toEqual({})
})

test('while the session id is still the old one, the timers retry and read nothing', async () => {
  const { hook, $, timers, fire, fetches } = setup()
  await hook($, END, async () => ({}))
  expect(timers.map(t => t.ms)).toEqual([CLEAR_RETRY_DELAYS_MS[0]])
  const fired: Array<number | undefined> = []
  for (let i = 0; i < 6; i++) fired.push(await fire())
  expect(fired).toEqual([...CLEAR_RETRY_DELAYS_MS, undefined, undefined])
  expect(fetches).toEqual({})
  expect(readCached('usage')).toBe(undefined) // stays cleared, never the old conversation's
  expect(readCached('compactionMarker')).toBe(undefined)
})

test('once the id changes, the name, mode and compaction marker are read once, usage is set, and the timers stop', async () => {
  const { hook, $, timers, fire, world, fetches } = setup()
  await hook($, END, async () => ({}))
  await fire() // id still old: a retry is queued
  world.id = 'new-id'
  world.usagePercent = 3
  await fire()
  expect(fetches).toEqual({ sessionName: 1, sessionMode: 1, compactionMarker: 1 })
  expect(readCached('sessionName')).toBe('sessionName-new')
  expect((readCached('usage') as { contextPercent?: number }).contextPercent).toBe(3)
  expect(timers).toEqual([])
  expect(readCached('gitHead')).toBe('gitHead-old')
})

test('an in-process resume is handled like a clear', async () => {
  const { hook, $, timers } = setup()
  await hook($, { reason: 'resume', sessionId: 'old-id' }, async () => ({}))
  expect(readCached('usage')).toBe(undefined)
  expect(timers).toHaveLength(1)
})

test('an end for any other reason leaves the values and starts no timer', async () => {
  const { hook, $, timers } = setup()
  for (const reason of ['prompt_input_exit', 'logout', 'other']) {
    await hook($, { reason, sessionId: 'old-id' }, async () => ({}))
  }
  expect((readCached('usage') as { contextPercent?: number }).contextPercent).toBe(62)
  expect(readCached('sessionName')).toBe('sessionName-old')
  expect(timers).toEqual([])
})

test('with the display disabled a clear does nothing, and a timer that finds it disabled reads nothing', async () => {
  const { hook, $, timers, fire, world, fetches } = setup()
  setOptions({ ...DEFAULT_OPTIONS, enable: false })
  await hook($, END, async () => ({}))
  expect(readCached('sessionName')).toBe('sessionName-old')
  expect(timers).toEqual([])
  // enabled at the clear, disabled before the timer fires
  setOptions(DEFAULT_OPTIONS)
  await hook($, END, async () => ({}))
  setOptions({ ...DEFAULT_OPTIONS, enable: false })
  world.id = 'new-id'
  await fire()
  expect(fetches).toEqual({})
  expect(readCached('usage')).toBe(undefined)
})

test('the hook clears before the engine end step runs and returns its result', async () => {
  const { hook, $ } = setup()
  let seenAtNext: unknown = 'not-run'
  const result = await hook($, END, async () => {
    seenAtNext = readCached('sessionName')
    return { sessionId: 'old-id' }
  })
  expect(seenAtNext).toBe(undefined)
  expect(result).toEqual({ sessionId: 'old-id' })
  const other = await hook($, { reason: 'other', sessionId: 'old-id' }, async () => ({ x: 1 }))
  expect(other).toEqual({ x: 1 })
})

test('a failing usage read keeps usage cleared, still refreshes the others and throws nothing', async () => {
  const { hook, $, fire, world, fetches } = setup()
  await hook($, END, async () => ({}))
  world.id = 'new-id'
  world.usageError = true
  await fire()
  expect(readCached('usage')).toBe(undefined)
  expect(fetches).toEqual({ sessionName: 1, sessionMode: 1, compactionMarker: 1 })
})

test('an id read that throws counts as not yet and retries', async () => {
  const { hook, $, timers, fire } = setup()
  ;($.session as { id: () => Promise<string> }).id = async () => {
    throw new Error('no id')
  }
  await hook($, END, async () => ({}))
  await fire()
  expect(timers.map(t => t.ms)).toEqual([CLEAR_RETRY_DELAYS_MS[1]])
})

test('the status.jsonl capture starts over: the new session file holds only its own record', async () => {
  const { hook, $, fire, world, stub } = setup()
  stub.state.cwd = '/work/r'
  stub.state.claudeConfigDir = '/cfg'
  stub.state.id = 'old-id'
  initLimitsCapture(stub.host, () => 1_000_000)
  const reading = (pct: number) => ({ limits: [{ kind: 'five_hour' as const, usedPercent: pct }] })
  captureLimits(reading(10))
  await flushLimitsCapture()
  expect(stub.writes.map(w => w.path)).toEqual(['/cfg/projects/-work-r/old-id.status.jsonl'])

  await hook($, END, async () => ({}))
  world.id = 'new-id'
  stub.state.id = 'new-id'
  await fire()
  captureLimits(reading(10)) // the same limits as before the clear: still written to the new file
  await flushLimitsCapture()
  const last = stub.writes[stub.writes.length - 1]!
  expect(last.path).toBe('/cfg/projects/-work-r/new-id.status.jsonl')
  expect(last.text.trim().split('\n')).toHaveLength(1)
  expect(last.text).toContain('"session_id":"new-id"')
})

test('a clear lets the next turn read the compaction marker again, so a never-seen id still heals', async () => {
  const { hook, $, fetches } = setup()
  readCompactionOnce() // the first turn.complete of the old session spent the one-time read
  await settle()
  expect(fetches.compactionMarker).toBe(1)
  await hook($, END, async () => ({}))
  readCompactionOnce()
  await settle()
  expect(fetches.compactionMarker).toBe(2)
})

test('a clear makes the Info modal read the session id again', async () => {
  const { hook, $ } = setup()
  let reads = 0
  const host = {
    session: {
      id: async () => {
        reads++
        return 'x'
      },
    },
  }
  resetSessionFacts()
  await loadSessionFacts(host as never)
  await loadSessionFacts(host as never)
  expect(reads).toBe(1) // memoised
  await hook($, END, async () => ({}))
  await loadSessionFacts(host as never)
  expect(reads).toBe(2)
})

test('an end event without the ending id takes it from $.session.id() before the clear', async () => {
  const { hook, $, fire, world, fetches } = setup()
  await hook($, { reason: 'clear' }, async () => ({}))
  await fire() // id is still the old one: nothing read
  expect(fetches).toEqual({})
  world.id = 'new-id'
  await fire()
  expect(fetches).toEqual({ sessionName: 1, sessionMode: 1, compactionMarker: 1 })
})

test('when the engine end step rejects, the clear already ran, the error propagates and the timers still start', async () => {
  const { hook, $, timers } = setup()
  let caught: unknown
  try {
    await hook($, END, async () => {
      throw new Error('end step failed')
    })
  } catch (err) {
    caught = err
  }
  expect((caught as Error).message).toBe('end step failed')
  expect(readCached('usage')).toBe(undefined)
  expect(timers).toHaveLength(1)
})

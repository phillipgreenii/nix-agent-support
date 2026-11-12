// Tests of the usage, model and effort sources, driven through the registration-time hooks the
// way the engine does ($ is a per-callback stub), and of the opt-out: with enable false the hooks
// leave the sources untouched.
import { expect, test } from 'claude-code/testing'
import { registerModelEffortHooks, startModelEffort } from './modelEffort'
import { setOptions } from './options'
import { defineSource, initSources, onChange, readCached, setValueIfChanged } from './source'
import { fakeClock, settle, stubHost } from './testkit'
import { DEFAULT_OPTIONS } from './types'
import { mapUsage, registerUsageHooks, startUsage } from './usage'

type Hook = (...a: unknown[]) => unknown
function collect(register: (on: unknown) => void): Record<string, Hook> {
  const hooks: Record<string, Hook> = {}
  register((name: string, fn: Hook) => {
    hooks[name] = fn
  })
  return hooks
}
const pass = (e: unknown): unknown => e

const RAW = {
  context: { percent: 42, tokens: 84000, window: 200000 },
  rateLimits: [
    { kind: 'five_hour', percentUsed: 12.5, resetsAt: '2026-11-06T20:00:00Z' },
    { kind: 'seven_day', percentUsed: 3 },
    { kind: 'spend_limit', percentUsed: 1 },
  ],
  cost: { usd: 1.25 },
}

test('mapUsage maps context, the two limit windows and cost', () => {
  expect(mapUsage(RAW)).toEqual({
    contextPercent: 42,
    contextTokens: 84000,
    limits: [
      { kind: 'five_hour', usedPercent: 12.5, resetsAt: '2026-11-06T20:00:00Z' },
      { kind: 'seven_day', usedPercent: 3 },
    ],
    costUsd: 1.25,
  })
  expect(mapUsage({})).toEqual({ limits: [] })
})

function boot(enable = true) {
  const clock = fakeClock()
  const s = stubHost()
  setOptions({ ...DEFAULT_OPTIONS, enable })
  initSources(s.host, { now: clock.now, setTimer: clock.setTimer })
  startUsage(s.host)
  startModelEffort(s.host)
  const hooks = {
    ...collect(registerUsageHooks as (on: unknown) => void),
    ...collect(registerModelEffortHooks as (on: unknown) => void),
  }
  return { clock, s, hooks }
}

test('session.measure updates the usage source from $.session.usage()', async () => {
  const { s, hooks } = boot()
  await hooks['session.measure']({ session: { usage: async () => RAW } }, {}, pass)
  expect((readCached('usage') as { contextPercent: number }).contextPercent).toBe(42)
  expect(s.calls.includes('process.run')).toBe(false)
})

test('the model is read at start and again after config.set', async () => {
  const { s, hooks } = boot()
  await settle()
  expect(readCached('model')).toBe('sonnet')
  await hooks['config.set']({ session: { model: async () => 'opus' } }, {}, async (e: unknown) => e)
  expect(readCached('model')).toBe('opus')
  void s
})

test('effort is blank until the first turn, then follows turn.step', async () => {
  const { hooks } = boot()
  expect(readCached('effort')).toBe(undefined)
  const gen = (hooks['turn.step'] as Hook)(undefined, { effort: 'high' }, function* (e: unknown) {
    return e
  }) as AsyncGenerator<unknown, unknown>
  for await (const _ of gen) void _
  expect(readCached('effort')).toBe('high')
})

test('a subagent step does not set the effort', async () => {
  const { hooks } = boot()
  const gen = (hooks['turn.step'] as Hook)(
    undefined,
    { effort: 'max', agentId: 'sub1' },
    function* (e: unknown) {
      return e
    },
  ) as AsyncGenerator<unknown, unknown>
  for await (const _ of gen) void _
  expect(readCached('effort')).toBe(undefined)
})

async function step(hooks: Record<string, Hook>, e: object): Promise<void> {
  const gen = hooks['turn.step'](undefined, e, function* (x: unknown) {
    return x
  }) as AsyncGenerator<unknown, unknown>
  for await (const _ of gen) void _
}

const modelReads = (s: { calls: string[] }): number => s.calls.filter(c => c === 'session.model').length

test('a changed model on a main-loop step re-reads session.model(), keeping its spelling', async () => {
  const { s, hooks } = boot()
  await settle()
  expect(readCached('model')).toBe('sonnet')
  s.state.model = 'opus' // what /model changed; the step names it by a full id
  await step(hooks, { model: 'claude-opus-full-id', effort: 'high' })
  await settle()
  expect(readCached('model')).toBe('opus')
  expect(readCached('effort')).toBe('high')
})

test('a subagent step neither re-reads the model nor sets the effort', async () => {
  const { s, hooks } = boot()
  await settle()
  const before = modelReads(s)
  s.state.model = 'haiku'
  await step(hooks, { model: 'haiku-full-id', effort: 'low', agentId: 'sub1' })
  await settle()
  expect(modelReads(s)).toBe(before)
  expect(readCached('model')).toBe('sonnet')
  expect(readCached('effort')).toBe(undefined)
})

test('a step without a model or effort leaves the cached values alone', async () => {
  const { s, hooks } = boot()
  await settle()
  await step(hooks, { model: 'sonnet-full-id', effort: 'high' })
  await settle()
  const before = modelReads(s)
  await step(hooks, {})
  await settle()
  expect(modelReads(s)).toBe(before)
  expect(readCached('model')).toBe('sonnet')
  expect(readCached('effort')).toBe('high')
})

test('a step naming the same model and effort again is a no-op: no read, no change', async () => {
  const { s, hooks } = boot()
  await settle()
  await step(hooks, { model: 'sonnet-full-id', effort: 'high' })
  await settle()
  const before = modelReads(s)
  let fired = 0
  onChange('model', () => fired++)
  onChange('effort', () => fired++)
  await step(hooks, { model: 'sonnet-full-id', effort: 'high' })
  await settle()
  expect(modelReads(s)).toBe(before)
  expect(fired).toBe(0)
})

test('config.set with an unchanged model is a no-op', async () => {
  const { hooks } = boot()
  await settle()
  let fired = 0
  onChange('model', () => fired++)
  await hooks['config.set']({ session: { model: async () => 'sonnet' } }, {}, async (e: unknown) => e)
  expect(fired).toBe(0)
})

test('setValueIfChanged stores a new value and ignores nil, equal and unknown-source values', () => {
  boot()
  defineSource<string>({ id: 'probe', intervalMs: 0 })
  expect(setValueIfChanged('probe', undefined)).toBe(false)
  expect(setValueIfChanged('probe', null)).toBe(false)
  expect(setValueIfChanged('probe', 'a')).toBe(true)
  expect(setValueIfChanged('probe', 'a')).toBe(false)
  expect(setValueIfChanged('probe', 'b')).toBe(true)
  expect(setValueIfChanged('no-such-source', 'x')).toBe(false)
})

test('with enable false the hooks leave the sources untouched', async () => {
  const { hooks } = boot(false)
  await hooks['session.measure']({ session: { usage: async () => RAW } }, {}, pass)
  expect(readCached('usage')).toBe(undefined)
})

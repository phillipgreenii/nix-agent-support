// Tests of the compaction marker: parsing, reading the file into the cached source (present,
// absent, malformed), the age window (recent and stale), the band's context label and the Context
// modal row.
import { expect, test } from 'claude-code/testing'
import { bandLabels } from './band'
import {
  COMPACTION_WINDOW_MS,
  compactionNote,
  currentCompactionNote,
  fetchCompaction,
  formatAge,
  markerPath,
  parseMarker,
  readCompactionOnce,
  refreshCompaction,
  setCompactionClock,
  startCompaction,
  type CompactionMarker,
} from './compaction'
import { contextSpec } from './context-modal'
import { setOptions } from './options'
import { initSources, readCached, defineSource, setValue } from './source'
import { DEFAULT_OPTIONS } from './types'
import { fakeClock, settle, stubHost } from './testkit'

const T = DEFAULT_OPTIONS.alerts.thresholds
const PATH = '/home/u/.claude/projects/-work-repo/sess-1.compaction.json'
const NOW = Date.UTC(2026, 10, 12, 12, 0, 0)
const marker = (ageMs: number, trigger = 'auto'): string =>
  JSON.stringify({ ts: Math.floor((NOW - ageMs) / 1000), trigger })

async function world(files: Record<string, string>) {
  const sh = stubHost(files)
  initSources(sh.host, fakeClock())
  setOptions(DEFAULT_OPTIONS)
  setCompactionClock(() => NOW)
  defineSource({ id: 'usage', intervalMs: 0 })
  startCompaction(sh.host)
  refreshCompaction()
  await settle()
  return sh
}

test('parseMarker accepts a positive integer ts and maps an odd trigger to unknown', () => {
  expect(parseMarker('{"ts":1760000000,"trigger":"manual"}')).toEqual({ ts: 1760000000, trigger: 'manual' })
  expect(parseMarker('{"ts":5}')).toEqual({ ts: 5, trigger: 'unknown' })
  expect(parseMarker('{"ts":5,"trigger":"x"}')).toEqual({ ts: 5, trigger: 'unknown' })
  expect(parseMarker('')).toBe(undefined)
  expect(parseMarker('not json')).toBe(undefined)
  expect(parseMarker('{"ts":0}')).toBe(undefined)
  expect(parseMarker('{"ts":-3}')).toBe(undefined)
  expect(parseMarker('{"ts":1.5}')).toBe(undefined)
  expect(parseMarker('{"ts":"7"}')).toBe(undefined)
  expect(parseMarker('null')).toBe(undefined)
})

test('the marker path is derived from the config dir, the cwd and the session id', async () => {
  const sh = stubHost()
  expect(await markerPath(sh.host)).toBe(PATH)
  sh.state.claudeConfigDir = '/cfg'
  sh.state.cwd = '/Volumes/a.b/repo'
  expect(await markerPath(sh.host)).toBe('/cfg/projects/-Volumes-a-b-repo/sess-1.compaction.json')
})

test('a present marker is read into the cached source and shows a note', async () => {
  await world({ [PATH]: marker(14 * 60_000 + 5_000) })
  expect(readCached<CompactionMarker>('compactionMarker')?.trigger).toBe('auto')
  expect(currentCompactionNote()).toBe('↻14m')
})

test('the first turn.complete read picks up a marker after a restart, and registration reads nothing', async () => {
  const sh = stubHost({ [PATH]: marker(60_000) })
  initSources(sh.host, fakeClock())
  setOptions(DEFAULT_OPTIONS)
  setCompactionClock(() => NOW)
  startCompaction(sh.host)
  await settle()
  expect(sh.calls).toEqual([])
  expect(readCached('compactionMarker')).toBe(undefined)
  readCompactionOnce()
  await settle()
  expect(currentCompactionNote()).toBe('↻1m')
})

test('an absent marker leaves no value and no note', async () => {
  await world({})
  expect(readCached('compactionMarker')).toBe(undefined)
  expect(currentCompactionNote()).toBe(undefined)
  const sh = stubHost()
  expect(await fetchCompaction(sh.host)).toBe(undefined)
})

test('a malformed or zero-byte marker leaves no value', async () => {
  await world({ [PATH]: '{"ts":' })
  expect(readCached('compactionMarker')).toBe(undefined)
  await world({ [PATH]: '' })
  expect(readCached('compactionMarker')).toBe(undefined)
})

test('a stale marker is cached but draws no note', async () => {
  await world({ [PATH]: marker(COMPACTION_WINDOW_MS + 1000) })
  expect(readCached<CompactionMarker>('compactionMarker') !== undefined).toBe(true)
  expect(currentCompactionNote()).toBe(undefined)
})

test('note ages: under a minute, minutes, hours, the window edge and clock skew', () => {
  expect(formatAge(30_000)).toBe('<1m')
  expect(formatAge(60_000)).toBe('1m')
  expect(formatAge(59 * 60_000)).toBe('59m')
  expect(formatAge(2 * 3600_000)).toBe('2h')
  expect(compactionNote(NOW - 1, NOW)).toBe('↻<1m')
  expect(compactionNote(NOW - COMPACTION_WINDOW_MS + 1, NOW)).toBe('↻29m')
  expect(compactionNote(NOW - COMPACTION_WINDOW_MS, NOW)).toBe(undefined)
  expect(compactionNote(NOW + 5_000, NOW)).toBe('↻<1m')
  expect(compactionNote(undefined, NOW)).toBe(undefined)
})

test('the band context label carries the note and omits it without one', () => {
  const ctx = (note?: string) =>
    bandLabels({ sessionId: 'abcdef123456', usage: { contextPercent: 12, limits: [] }, compactNote: note }, T).find(
      l => l.id === 'context',
    )?.label
  expect(ctx('↻14m')).toBe('ctx 12% ↻14m')
  expect(ctx()).toBe('ctx 12%')
})

test('the Context modal shows the marker time after a restart (no in-memory event)', async () => {
  await world({ [PATH]: marker(60_000) })
  setValue('usage', { contextPercent: 10, limits: [] })
  const rows = contextSpec().items.map(i => `${i.label}: ${i.value}`)
  expect(rows).toEqual(['context: 10%', 'last compaction: 2026-11-12 11:59 UTC'])
})

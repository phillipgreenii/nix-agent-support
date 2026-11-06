// Tests of the session name source: sample registry entries read through the stub facade (no
// process, no transcript), the once-per-10-seconds re-read after turn.complete, the freedom-boundary
// choices, and the registration path (components.ts and the turn.complete hook).
import { expect, test } from 'claude-code/testing'
import { bandLabels } from './band'
import { registerComponents } from './components'
import { registerGitHeadHooks } from './gitHead'
import { setOptions } from './options'
import { initSources, readCached } from './source'
import { fetchSessionName, resetSessionName } from './session-name'
import { fakeClock, settle, stubHost } from './testkit'
import { DEFAULT_OPTIONS } from './types'
import { ENTRY_DERIVED, ENTRY_MALFORMED, ENTRY_OTHER, ENTRY_RENAMED, ENTRY_USER } from './fixtures/registry'

const DIR = '/home/u/.claude/sessions'

test('a user-named entry supplies its name', async () => {
  resetSessionName()
  const s = stubHost({ [`${DIR}/4242.json`]: ENTRY_USER })
  expect(await fetchSessionName(s.host)).toBe('drain-bead')
})

test('a derived entry supplies its name too', async () => {
  resetSessionName()
  const s = stubHost({ [`${DIR}/4242.json`]: ENTRY_DERIVED })
  expect(await fetchSessionName(s.host)).toBe('repo')
})

test('every nameSource supplies the name, whatever its value', async () => {
  for (const nameSource of ['user', 'derived', 'auto', 'collision', 'some-future-source']) {
    resetSessionName()
    const entry = JSON.stringify({ pid: 4242, sessionId: 'sess-1', name: `n-${nameSource}`, nameSource })
    expect(await fetchSessionName(stubHost({ [`${DIR}/4242.json`]: entry }).host)).toBe(`n-${nameSource}`)
  }
})

test('the id is shown until the entry is written, then the name replaces it', async () => {
  resetSessionName()
  const s = stubHost()
  expect(await fetchSessionName(s.host)).toBe(undefined)
  expect(bandLabels({ sessionId: 'sess-1-long-id', sessionName: undefined }, DEFAULT_OPTIONS.alerts.thresholds)[0].label).toBe('sess-1-l')
  s.files.set(`${DIR}/4242.json`, ENTRY_DERIVED)
  expect(await fetchSessionName(s.host)).toBe('repo')
})

test('a missing registry, a missing entry and a malformed entry all give the id', async () => {
  resetSessionName()
  expect(await fetchSessionName(stubHost().host)).toBe(undefined)
  resetSessionName()
  expect(await fetchSessionName(stubHost({ [`${DIR}/9.json`]: ENTRY_OTHER }).host)).toBe(undefined)
  resetSessionName()
  expect(await fetchSessionName(stubHost({ [`${DIR}/4242.json`]: ENTRY_MALFORMED }).host)).toBe(undefined)
})

test('an entry for another sessionId is ignored, the matching one is used', async () => {
  resetSessionName()
  const s = stubHost({ [`${DIR}/1.json`]: ENTRY_OTHER, [`${DIR}/4242.json`]: ENTRY_USER, [`${DIR}/note.txt`]: 'x' })
  expect(await fetchSessionName(s.host)).toBe('drain-bead')
})

test('the band first row shows the name when one exists and the id otherwise', () => {
  const t = DEFAULT_OPTIONS.alerts.thresholds
  expect(bandLabels({ sessionId: 'abcdefghijkl', sessionName: 'drain-bead' }, t)[0]).toEqual({
    id: 'session',
    label: 'drain-bead',
    action: 'info',
  })
  expect(bandLabels({ sessionId: 'abcdefghijkl' }, t)[0].label).toBe('abcdefgh')
})

test('the entry is located once, its path cached, and later reads touch only that path', async () => {
  resetSessionName()
  const s = stubHost({ [`${DIR}/1.json`]: ENTRY_OTHER, [`${DIR}/4242.json`]: ENTRY_USER })
  await fetchSessionName(s.host)
  expect(s.calls.filter(c => c.startsWith('fs.list')).length).toBe(1)
  s.calls.length = 0
  s.files.set(`${DIR}/4242.json`, ENTRY_RENAMED)
  expect(await fetchSessionName(s.host)).toBe('renamed')
  expect(s.calls).toEqual([`fs.read ${DIR}/4242.json`])
})

test('a failed read of the cached path discovers again; a reused path of another session too', async () => {
  resetSessionName()
  const s = stubHost({ [`${DIR}/4242.json`]: ENTRY_USER })
  await fetchSessionName(s.host)
  s.files.delete(`${DIR}/4242.json`)
  s.files.set(`${DIR}/5555.json`, ENTRY_RENAMED)
  expect(await fetchSessionName(s.host)).toBe('renamed')
  s.files.set(`${DIR}/5555.json`, ENTRY_OTHER)
  s.files.set(`${DIR}/6666.json`, ENTRY_USER)
  expect(await fetchSessionName(s.host)).toBe('drain-bead')
})

test('a malformed entry on the cached path gives the id and keeps the path', async () => {
  resetSessionName()
  const s = stubHost({ [`${DIR}/4242.json`]: ENTRY_USER })
  await fetchSessionName(s.host)
  s.files.set(`${DIR}/4242.json`, ENTRY_MALFORMED)
  s.calls.length = 0
  expect(await fetchSessionName(s.host)).toBe(undefined)
  expect(s.calls).toEqual([`fs.read ${DIR}/4242.json`])
  s.files.set(`${DIR}/4242.json`, ENTRY_USER)
  expect(await fetchSessionName(s.host)).toBe('drain-bead')
})

test('no process, no store and no transcript read: only the registry directory and its entries', async () => {
  resetSessionName()
  const s = stubHost({ [`${DIR}/4242.json`]: ENTRY_USER })
  await fetchSessionName(s.host)
  expect(s.calls.every(c => c.startsWith(`fs.list ${DIR}`) || c.startsWith(`fs.read ${DIR}/`))).toBe(true)
})

test('registered by components.ts: read at start, and re-read after turn.complete at most once per 10 seconds', async () => {
  resetSessionName()
  const clock = fakeClock()
  const s = stubHost({ [`${DIR}/4242.json`]: ENTRY_USER })
  initSources(s.host, clock)
  setOptions(DEFAULT_OPTIONS)
  registerComponents(DEFAULT_OPTIONS, s.host)
  const hooks: Record<string, (...a: unknown[]) => unknown> = {}
  registerGitHeadHooks((n: string, fn: (...a: unknown[]) => unknown) => void (hooks[n] = fn))
  await clock.advance(0)
  await settle()
  expect(readCached('sessionName')).toBe('drain-bead')
  const reads = (): number => s.calls.filter(c => c === `fs.read ${DIR}/4242.json`).length
  const base = reads()
  s.files.set(`${DIR}/4242.json`, ENTRY_RENAMED)
  hooks['turn.complete'](undefined, {}, (e: unknown) => e) // inside the 10 s throttle
  await clock.advance(5000)
  expect(reads()).toBe(base)
  expect(readCached('sessionName')).toBe('drain-bead')
  await clock.advance(5000)
  expect(reads()).toBe(base + 1)
  expect(readCached('sessionName')).toBe('renamed')
  hooks['turn.complete'](undefined, {}, (e: unknown) => e) // inside the new throttle window
  await settle()
  expect(reads()).toBe(base + 1)
})

test('with components.sessionName off no registry file is read and the id is shown', async () => {
  resetSessionName()
  const clock = fakeClock()
  const s = stubHost({ [`${DIR}/4242.json`]: ENTRY_USER })
  initSources(s.host, clock)
  const off = { ...DEFAULT_OPTIONS, components: { ...DEFAULT_OPTIONS.components, sessionName: false } }
  setOptions(off)
  registerComponents(off, s.host)
  const hooks: Record<string, (...a: unknown[]) => unknown> = {}
  registerGitHeadHooks((n: string, fn: (...a: unknown[]) => unknown) => void (hooks[n] = fn))
  hooks['turn.complete'](undefined, {}, (e: unknown) => e)
  await clock.advance(30000)
  expect(s.calls.some(c => c.startsWith('fs.'))).toBe(false)
  expect(readCached('sessionName')).toBe(undefined)
  expect(bandLabels({ sessionId: 'abcdefghijkl', sessionName: readCached<string>('sessionName') }, off.alerts.thresholds)[0].label).toBe('abcdefgh')
})

// Tests of the PR source: the gh pr view argument vector (repo and branch passed explicitly, so no
// git is needed), the five-minute throttle and the one fetch per branch change, sharing through the
// store, opt-out, the freedom-boundary choices documented in the README, and a run of the REAL gh
// binary in a hermetic environment (throwaway HOME and config directory, no token, no network).
import { expect, test } from 'claude-code/testing'
import { registerComponents } from './components'
import { startGitHead } from './gitHead'
import { infoSpec, resetSessionFacts } from './info-modal'
import { setOptions } from './options'
import { PR_INTERVAL_MS, prArgv, prKey, repoSlugOf } from './pr'
import { initSources, readCached, setValue, setVisible } from './source'
import { boot, HEAD, ok, tick, withPr } from './prkit'

import { fakeClock, settle, stubHost } from './testkit'

const gh = (cmds: string[][]): string[][] => cmds.filter(c => c[0] === 'gh')

test('the gh argument vector carries -R owner/repo and the branch, and every command is gh', async () => {
  const { s, commands } = await boot({ branch: 'feature/x' })
  expect(commands).toEqual([['gh', 'pr', 'view', 'feature/x', '-R', 'owner/repo', '--json', 'number,reviewDecision']])
  const argv = commands[0]
  expect(argv[argv.indexOf('-R') + 1]).toBe('owner/repo')
  expect(argv.includes('feature/x')).toBe(true)
  expect(commands.every(c => c[0] === 'gh')).toBe(true)
  expect(readCached('pr')).toEqual({ number: 12, reviewState: 'APPROVED' })
  expect(prKey()).toBe('owner/repo#feature/x')
  void s
})

test('prArgv and repoSlugOf: the repo is the name, else parsed from the remote', () => {
  expect(prArgv('o/r', 'b')).toEqual(['gh', 'pr', 'view', 'b', '-R', 'o/r', '--json', 'number,reviewDecision'])
  expect(repoSlugOf({ name: 'o/r', remote: null })).toBe('o/r')
  expect(repoSlugOf({ name: 'r', remote: 'git@github.com:o/r.git' })).toBe('o/r')
  expect(repoSlugOf({ name: null, remote: 'https://github.com/o/r' })).toBe('o/r')
  expect(repoSlugOf({ name: null, remote: null })).toBe(undefined)
  expect(repoSlugOf(null)).toBe(undefined)
})

test('the PR is fetched at most every five minutes, and once more when the branch changes', async () => {
  const { clock, s, commands } = await boot({ branch: 'feature/x' })
  expect(gh(commands)).toHaveLength(1)
  await tick(clock, PR_INTERVAL_MS - 10_000)
  expect(gh(commands)).toHaveLength(1)
  await tick(clock, 20_000)
  expect(gh(commands)).toHaveLength(2)
  // the branch changes: the next gitHead poll sees it and one forced fetch follows, inside the
  // throttle window, clearing the old branch's value first
  s.files.set('/work/repo/.git/HEAD', HEAD('other'))
  await clock.advance(10_000)
  expect(gh(commands)).toHaveLength(3)
  expect(commands[2][3]).toBe('other')
  await clock.advance(30_000)
  expect(gh(commands)).toHaveLength(3)
})

test('a branch change clears the old value at once, before the new fetch answers', async () => {
  let release: (v: unknown) => void = () => undefined
  const { clock, s } = await boot({ branch: 'a' })
  expect(readCached('pr')).toEqual({ number: 12, reviewState: 'APPROVED' })
  s.host.process.run = () => new Promise(r => (release = r))
  s.files.set('/work/repo/.git/HEAD', HEAD('b'))
  await clock.advance(10_000)
  expect(readCached('pr')).toBe(undefined)
  release({ exitCode: 0, stdout: JSON.stringify({ number: 7, reviewDecision: '' }) })
  await settle()
  expect(readCached('pr')).toEqual({ number: 7, reviewState: '' })
})

test('a branch change while the Info modal is closed is fetched when it next opens', async () => {
  const { clock, s, commands } = await boot({ branch: 'a' })
  setVisible('modal', false)
  s.files.set('/work/repo/.git/HEAD', HEAD('b'))
  await clock.advance(20_000)
  expect(gh(commands)).toHaveLength(1)
  expect(readCached('pr')).toBe(undefined)
  setVisible('modal', true)
  await clock.advance(1)
  expect(gh(commands)).toHaveLength(2)
  expect(commands[1][3]).toBe('b')
})

test('with prStatus off there is no gh source, no timer and no PR row', async () => {
  const clock = fakeClock()
  const s = stubHost()
  const options = withPr(false)
  setOptions(options)
  resetSessionFacts()
  initSources(s.host, { now: clock.now, setTimer: clock.setTimer })
  registerComponents(options, s.host)
  setVisible('modal', true)
  await clock.advance(PR_INTERVAL_MS * 2)
  expect(s.calls.filter(c => c === 'process.run')).toHaveLength(0)
  expect(clock.pending()).toBe(0)
  expect(readCached('pr')).toBe(undefined)
  setValue('pr', { number: 1, reviewState: 'APPROVED' }) // no source: a no-op
  expect(infoSpec().items.some(i => i.label === 'PR')).toBe(false)
})

test('a session that finds the shared entry fresh does not fetch; an expired entry is fetched', async () => {
  const a = await boot({ branch: 'feature/x' })
  expect(gh(a.commands)).toHaveLength(1)
  expect(a.s.storeData.has('shared:pr:owner/repo#feature/x')).toBe(true)
  // a second session on the same store, one minute later
  const b = await boot({ s: a.s, run: async () => ({ exitCode: 1 }) })
  expect(gh(b.commands)).toHaveLength(0)
  expect(readCached('pr')).toEqual({ number: 12, reviewState: 'APPROVED' })
  // the entry is older than five minutes for a third session
  const entry = a.s.storeData.get('shared:pr:owner/repo#feature/x') as { value: unknown; fetchedAt: number }
  entry.fetchedAt -= PR_INTERVAL_MS + 1
  const c = await boot({ s: a.s, run: ok(99, 'CHANGES_REQUESTED') })
  expect(gh(c.commands)).toHaveLength(1)
  expect(readCached('pr')).toEqual({ number: 99, reviewState: 'CHANGES_REQUESTED' })
})

test('two sessions on different branches of one repo do not read each other\'s entry', async () => {
  const a = await boot({ branch: 'one', run: ok(1, 'APPROVED') })
  const b = await boot({ s: a.s, branch: 'two', run: ok(2, 'REVIEW_REQUIRED') })
  expect(gh(b.commands)).toHaveLength(1)
  expect(readCached('pr')).toEqual({ number: 2, reviewState: 'REVIEW_REQUIRED' })
  expect([...a.s.storeData.keys()].sort()).toEqual(['shared:pr:owner/repo#one', 'shared:pr:owner/repo#two'])
})

test('a failed gh call omits the PR row and changes nothing else', async () => {
  const s = stubHost({ '/work/repo/.git/HEAD': HEAD('feature/x') })
  await boot({ s, run: ok() })
  expect(infoSpec().items.some(i => i.label === 'PR')).toBe(true)
  const before = JSON.stringify({ git: readCached('gitHead'), model: readCached('model') })
  s.storeData.clear() // no shared entry: the second session must run gh itself
  const failing = await boot({ s, run: async () => Promise.reject(new Error('gh: not logged in')) })
  expect(gh(failing.commands)).toHaveLength(1)
  expect(readCached('pr')).toBe(undefined)
  expect(infoSpec().items.some(i => i.label === 'PR')).toBe(false)
  expect(JSON.stringify({ git: readCached('gitHead'), model: readCached('model') })).toBe(before)
})

test('a non-zero exit, unparseable output and a missing number are failures with a backoff', async () => {
  for (const run of [
    async () => ({ exitCode: 1, stdout: '' }),
    async () => ({ exitCode: 0, stdout: 'not json' }),
    async () => ({ exitCode: 0, stdout: '{}' }),
  ]) {
    const { clock, commands } = await boot({ branch: 'feature/x', run })
    expect(readCached('pr')).toBe(undefined)
    expect(gh(commands)).toHaveLength(1)
    // doubling backoff: no second attempt at the plain interval, one by twice that
    await tick(clock, PR_INTERVAL_MS + 1000)
    expect(gh(commands)).toHaveLength(1)
    await tick(clock, PR_INTERVAL_MS)
    expect(gh(commands)).toHaveLength(2)
  }
})

test('no branch (detached head) or no derivable repo: no fetch and no PR row', async () => {
  const detached = stubHost({ '/work/repo/.git/HEAD': '0123456789abcdef0123456789abcdef01234567\n' })
  const d = await boot({ s: detached })
  await d.clock.advance(PR_INTERVAL_MS)
  expect(gh(d.commands)).toHaveLength(0)
  expect(readCached('pr')).toBe(undefined)
  expect(prKey()).toBe(undefined)
  const noRepo = await boot({ branch: 'feature/x', repo: null })
  await noRepo.clock.advance(PR_INTERVAL_MS)
  expect(gh(noRepo.commands)).toHaveLength(0)
  expect(infoSpec().items.some(i => i.label === 'PR')).toBe(false)
})

test('an empty review decision keeps the value with an empty state: the row shows the number alone', async () => {
  await boot({ branch: 'feature/x', run: ok(5, '') })
  expect(readCached('pr')).toEqual({ number: 5, reviewState: '' })
  expect(infoSpec().items.find(i => i.label === 'PR')).toEqual({ label: 'PR', value: '#5' })
})

test('the review state is coloured: approved green, changes requested red, review required yellow', async () => {
  const expected: Array<[string, string, string | undefined]> = [
    ['APPROVED', '#12 (approved)', 'green'],
    ['CHANGES_REQUESTED', '#12 (changes requested)', 'red'],
    ['REVIEW_REQUIRED', '#12 (review required)', 'yellow'],
  ]
  for (const [state, value, color] of expected) {
    await boot({ branch: 'feature/x', run: ok(12, state) })
    expect(infoSpec().items.find(i => i.label === 'PR')).toEqual({ label: 'PR', value, color })
  }
})

test('a draw starts no fetch, process or file read', async () => {
  const { s } = await boot({ branch: 'feature/x' })
  const before = s.calls.length
  infoSpec()
  readCached('pr')
  expect(s.calls.length).toBe(before)
})

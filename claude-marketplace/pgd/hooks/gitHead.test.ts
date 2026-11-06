// Tests of the .git source: the pure parser against fixture files (a branch, a detached head, an
// absolute and a relative worktree pointer) and the fetch, which reads only through host.fs and
// never runs a process.
import { expect, test } from 'claude-code/testing'
import { fetchGitHead, parseGitHead, registerGitHeadHooks, startGitHead } from './gitHead'
import { setOptions } from './options'
import { initSources, readCached } from './source'
import { fakeClock, stubHost } from './testkit'
import { DEFAULT_OPTIONS } from './types'
import { HEAD_BRANCH, HEAD_DETACHED, POINTER_ABSOLUTE, POINTER_RELATIVE } from './fixtures/git'

test('parseGitHead reads a branch', () => {
  expect(parseGitHead(HEAD_BRANCH)).toEqual({ branch: 'feature/source-layer' })
})

test('parseGitHead reads a detached head', () => {
  expect(parseGitHead(HEAD_DETACHED)).toEqual({
    detached: '0123456789abcdef0123456789abcdef01234567',
  })
})

test('parseGitHead reads an absolute worktree pointer', () => {
  expect(parseGitHead(HEAD_BRANCH, POINTER_ABSOLUTE, '/work/wt1')).toEqual({
    branch: 'feature/source-layer',
    worktree: '/repos/main/.git/worktrees/wt1',
  })
})

test('parseGitHead resolves a relative worktree pointer against the base directory', () => {
  expect(parseGitHead(HEAD_BRANCH, POINTER_RELATIVE, '/work/wt2')).toEqual({
    branch: 'feature/source-layer',
    worktree: '/work/main/.git/worktrees/wt2',
  })
})

test('fetchGitHead reads a plain checkout without a worktree value', async () => {
  const s = stubHost({ '/work/repo/.git/HEAD': HEAD_BRANCH })
  expect(await fetchGitHead(s.host)).toEqual({ branch: 'feature/source-layer' })
})

test('fetchGitHead follows a .git pointer file to the worktree git directory', async () => {
  const s = stubHost({
    '/work/repo/.git': POINTER_ABSOLUTE,
    '/repos/main/.git/worktrees/wt1/HEAD': HEAD_DETACHED,
  })
  expect(await fetchGitHead(s.host)).toEqual({
    detached: '0123456789abcdef0123456789abcdef01234567',
    worktree: '/repos/main/.git/worktrees/wt1',
  })
})

test('a missing .git is an undefined value, not a failure, and no source runs a git command', async () => {
  const s = stubHost()
  expect(await fetchGitHead(s.host)).toBe(undefined)
  expect(s.calls.includes('process.run')).toBe(false)
})

test('the gitHead source fetches through the facade only and never runs a process', async () => {
  const clock = fakeClock()
  const s = stubHost({ '/work/repo/.git/HEAD': HEAD_BRANCH })
  setOptions(DEFAULT_OPTIONS)
  initSources(s.host, { now: clock.now, setTimer: clock.setTimer })
  startGitHead(s.host)
  await clock.advance(35_000)
  expect(readCached('gitHead')).toEqual({ branch: 'feature/source-layer' })
  expect(s.calls.filter(c => c === 'process.run')).toHaveLength(0)
  expect(s.calls.filter(c => c.startsWith('fs.read') && !c.endsWith('HEAD') && !c.endsWith('.git'))).toHaveLength(0)
})

test('gitHead is fetched at most once per 10 seconds, and again after turn.complete', async () => {
  const clock = fakeClock()
  const s = stubHost({ '/work/repo/.git/HEAD': HEAD_BRANCH })
  setOptions(DEFAULT_OPTIONS)
  initSources(s.host, { now: clock.now, setTimer: clock.setTimer })
  const hooks: Record<string, (...a: unknown[]) => unknown> = {}
  registerGitHeadHooks((name: string, fn: (...a: unknown[]) => unknown) => {
    hooks[name] = fn
  })
  startGitHead(s.host)
  const heads = (): number => s.calls.filter(c => c === 'fs.read /work/repo/.git/HEAD').length
  await clock.advance(0)
  expect(heads()).toBe(1)
  await clock.advance(4000)
  expect(heads()).toBe(1)
  hooks['turn.complete'](undefined, {}, (e: unknown) => e) // inside the 10 s throttle
  await clock.advance(0)
  expect(heads()).toBe(1)
  await clock.advance(7000)
  expect(heads()).toBe(2)
  await clock.advance(10_000)
  hooks['session.compact'](undefined, {}, (e: unknown) => e)
  await clock.advance(0)
  expect(heads()).toBe(3)
})

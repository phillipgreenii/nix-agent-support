// Tests of the link context snapshot: the synchronous seed, the forced refreshes at session start
// and on every turn.complete, a changed cwd and repo, a failed host read and the invalidate count.
import { expect, test } from 'claude-code/testing'
import { registerGitHeadHooks } from './gitHead'
import { linkContext, resetLinkContext, startLinkContext } from './linkContext'
import { registerKind, resetLinkMemo, rewriteLinks } from './links'
import { setOptions } from './options'
import { initSources, refresh } from './source'
import { fakeClock, settle, stubHost } from './testkit'
import { DEFAULT_OPTIONS, type PgdOptions } from './types'

const options: PgdOptions = {
  ...DEFAULT_OPTIONS,
  github: { host: 'github.example.invalid', repos: ['Acme/widgets', 'acme/gadgets', 'other/thing'] },
}

// A stand-in kind that needs the snapshot, so the tests can see what a render would be given.
registerKind('ctx-probe', () => ({
  scan: text => [...text.matchAll(/[\w.-]+\/[\w.-]+#\d+|\/[\w/.-]+/g)].map(m => ({
    start: m.index!,
    end: m.index! + m[0].length,
    text: m[0],
  })),
  resolve: (c, _t, ctx) => {
    if (c.text.startsWith('/')) return `file://${c.text}`
    const [repo, n] = c.text.split('#')
    const owner = repo.split('/')[0].toLowerCase()
    if (!ctx?.githubOwners?.some(o => o.toLowerCase() === owner)) return { decline: 'unknown-repo' }
    return `https://${ctx.githubHost}/${repo}/issues/${n}`
  },
}))
const probe = [{ kind: 'ctx-probe', name: 'p' }] as any

function setup() {
  const clock = fakeClock()
  const s = stubHost()
  setOptions(options)
  initSources(s.host, { now: clock.now, setTimer: clock.setTimer })
  resetLinkMemo()
  startLinkContext(options, s.host)
  return s
}
const invalidates = (s: ReturnType<typeof stubHost>): number => s.calls.filter(c => c === 'ui.invalidate').length

test('the seed holds the github fields before any fetch, and token-complete forms link', () => {
  setup()
  const c = linkContext()
  expect(c.cwd).toBe(undefined)
  expect(c.githubHost).toBe('github.example.invalid')
  expect(c.githubOwners).toEqual(['Acme', 'other'])
  expect(c.knownRepos).toEqual(['Acme/widgets', 'acme/gadgets', 'other/thing'])
  expect(rewriteLinks('see ACME/widgets#7 and /abs/x.go', probe, c)).toBe(
    'see [ACME/widgets#7](https://github.example.invalid/ACME/widgets/issues/7) and [/abs/x.go](file:///abs/x.go)',
  )
  expect(rewriteLinks('see nobody/x#7', probe, c)).toBe('see nobody/x#7')
})

test('a forced refresh adds cwd, repoSlug and home and keeps the seeded fields', async () => {
  const s = setup()
  refresh('linkContext', { force: true })
  await settle()
  expect(linkContext()).toEqual({
    cwd: '/work/repo',
    repoSlug: 'owner/repo',
    home: '/home/u',
    githubHost: 'github.example.invalid',
    githubOwners: ['owner', 'Acme', 'other'],
    knownRepos: ['owner/repo', 'Acme/widgets', 'acme/gadgets', 'other/thing'],
  })
  expect(invalidates(s)).toBe(2) // the seed, then the fetch that added cwd, slug and home
})

test('turn.complete forces a refresh and so sees a changed cwd and a changed repo', async () => {
  const s = setup()
  const hooks: Record<string, (...a: unknown[]) => unknown> = {}
  registerGitHeadHooks((name: string, fn: (...a: unknown[]) => unknown) => {
    hooks[name] = fn
  })
  refresh('linkContext', { force: true })
  await settle()
  s.state.cwd = '/work/other'
  s.facts.repo = { root: '/work/other', remote: 'git@h.invalid:Acme/Widgets.git', name: null }
  expect(linkContext().cwd).toBe('/work/repo') // nothing changes until a trigger
  hooks['turn.complete'](undefined, {}, (e: unknown) => e)
  await settle()
  expect(linkContext().cwd).toBe('/work/other')
  expect(linkContext().repoSlug).toBe('Acme/Widgets')
  expect(linkContext().knownRepos).toEqual(['Acme/Widgets', 'acme/gadgets', 'other/thing'])
  expect(linkContext().knownRepos?.includes('owner/repo')).toBe(false)
  expect(invalidates(s)).toBe(3) // the seed and the two fetches
})

test('a failed host read keeps that field and still refreshes the others', async () => {
  const s = setup()
  refresh('linkContext', { force: true })
  await settle()
  s.host.session.cwd = async () => {
    throw new Error('gone')
  }
  s.host.session.repo = async () => {
    throw new Error('gone')
  }
  s.host.env.home = async () => {
    throw new Error('gone')
  }
  const before = invalidates(s)
  refresh('linkContext', { force: true })
  await settle()
  expect(linkContext().cwd).toBe('/work/repo')
  expect(linkContext().repoSlug).toBe('owner/repo')
  expect(linkContext().home).toBe('/home/u')
  expect(invalidates(s)).toBe(before)
})

test('a refresh that finds nothing new does not invalidate', async () => {
  const s = setup()
  refresh('linkContext', { force: true })
  await settle()
  const n = invalidates(s)
  refresh('linkContext', { force: true })
  await settle()
  refresh('linkContext', { force: true })
  await settle()
  expect(invalidates(s)).toBe(n)
})

test('a repo read that answers no repo clears the slug', async () => {
  const s = setup()
  refresh('linkContext', { force: true })
  await settle()
  s.facts.repo = null
  refresh('linkContext', { force: true })
  await settle()
  expect(linkContext().repoSlug).toBe(undefined)
  expect(linkContext().knownRepos).toEqual(['Acme/widgets', 'acme/gadgets', 'other/thing'])
})

test('the getter returns a copy and a restart resets the snapshot', () => {
  setup()
  linkContext().knownRepos!.push('x/y')
  expect(linkContext().knownRepos).toEqual(['Acme/widgets', 'acme/gadgets', 'other/thing'])
  resetLinkContext()
  expect(linkContext()).toEqual({})
})

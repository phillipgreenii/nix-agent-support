// Tests of the github-pr link kind, through the real rewrite with real strings.
import { expect, test } from 'claude-code/testing'
import { registerGithubPrKind } from './githubPr'
import { resetLinkMemo, rewriteLinks, type LinkContext, type LinkRule } from './links'

registerGithubPrKind()

const rule = (sessionRepoFallback = false): LinkRule => ({
  kind: 'github-pr',
  name: 'pr',
  options: { sessionRepoFallback },
})
const ctx: LinkContext = {
  githubOwners: ['acme'],
  knownRepos: ['acme/widgets', 'acme/gadgets'],
  repoSlug: 'acme/gadgets',
}
const run = (text: string, fallback = false, context: LinkContext = ctx): string => {
  resetLinkMemo()
  return rewriteLinks(text, [rule(fallback)], context)
}
const link = (text: string, repo: string, n: string): string =>
  `[${text}](https://github.com/${repo}/pull/${n})`

test('explicit owner/repo#N links with the whole token as text', () => {
  expect(run('see acme/widgets#5 now')).toBe(`see ${link('acme/widgets#5', 'acme/widgets', '5')} now`)
  // comparison ignores case, the link keeps the case the text used
  expect(run('see Acme/Widgets#5')).toBe(`see ${link('Acme/Widgets#5', 'Acme/Widgets', '5')}`)
})

test('PR forms link against the nearest earlier owner/repo mention', () => {
  const lead = 'In acme/widgets we merged '
  expect(run(`${lead}PR #123.`)).toBe(`${lead}${link('PR #123', 'acme/widgets', '123')}.`)
  expect(run(`${lead}PR 7`)).toBe(`${lead}${link('PR 7', 'acme/widgets', '7')}`)
  expect(run(`${lead}pull request 7`)).toBe(`${lead}${link('pull request 7', 'acme/widgets', '7')}`)
  expect(run(`${lead}Pull Request #5`)).toBe(`${lead}${link('Pull Request #5', 'acme/widgets', '5')}`)
  expect(run(`${lead}pull #8`)).toBe(`${lead}${link('pull #8', 'acme/widgets', '8')}`)
  expect(run('acme/widgets is busy. Then PR 9')).toBe(`acme/widgets is busy. Then ${link('PR 9', 'acme/widgets', '9')}`)
})

test('the nearest earlier mention wins and a new paragraph forgets the old one', () => {
  expect(run('acme/widgets and acme/gadgets: PR 3')).toBe(`acme/widgets and acme/gadgets: ${link('PR 3', 'acme/gadgets', '3')}`)
  expect(run('acme/widgets\n\nPR 3')).toBe('acme/widgets\n\nPR 3')
  expect(run('acme/widgets\nPR 3')).toBe(`acme/widgets\n${link('PR 3', 'acme/widgets', '3')}`)
  // a bare repo name is never a mention, and an unknown repo is not one either
  expect(run('widgets has PR 3')).toBe('widgets has PR 3')
  expect(run('other/thing has PR 3')).toBe('other/thing has PR 3')
})

test('text that is not a reference stays plain', () => {
  const lead = 'acme/widgets: '
  for (const s of ['PRs 7', 'GPR 9', 'pr 5 times', '#123', 'PR', 'PR seven', 'PR 7abc', 'xpull #3']) {
    expect(run(lead + s)).toBe(lead + s)
  }
  expect(run('src/utils#3')).toBe('src/utils#3')
  expect(run('owner/repo#N')).toBe('owner/repo#N')
})

test('an explicit owner/repo with an unknown owner declines', () => {
  expect(run('see other/widgets#5', true)).toBe('see other/widgets#5')
})

test('a reference inside a code span stays plain', () => {
  expect(run('acme/widgets `PR 7` and `acme/widgets#5`')).toBe('acme/widgets `PR 7` and `acme/widgets#5`')
})

test('the session repo is used only with sessionRepoFallback', () => {
  expect(run('merged PR 12')).toBe('merged PR 12')
  expect(run('merged PR 12', true)).toBe(`merged ${link('PR 12', 'acme/gadgets', '12')}`)
  // a mention still beats the fallback
  expect(run('acme/widgets: PR 12', true)).toBe(`acme/widgets: ${link('PR 12', 'acme/widgets', '12')}`)
  // no session repo: the fallback has nothing to use
  expect(run('merged PR 12', true, { githubOwners: ['acme'], knownRepos: [] })).toBe('merged PR 12')
})

test('a candidate in a path declines as adjacent', () => {
  expect(run('src/PR 5')).toBe('src/PR 5')
  expect(run('x/acme/widgets#5')).toBe('x/acme/widgets#5')
  expect(run('acme/widgets#5.go')).toBe('acme/widgets#5.go')
  expect(run('acme/widgets#5:12')).toBe('acme/widgets#5:12')
  // a sentence end and a colon not followed by a digit are not paths
  expect(run('see acme/widgets#5. Then')).toBe(`see ${link('acme/widgets#5', 'acme/widgets', '5')}. Then`)
  expect(run('see acme/widgets#5: done')).toBe(`see ${link('acme/widgets#5', 'acme/widgets', '5')}: done`)
})

test('without a context nothing links, and the host comes from the context', () => {
  expect(rewriteLinks('acme/widgets#5', [rule()])).toBe('acme/widgets#5')
  expect(run('acme/widgets#5', false, { ...ctx, githubHost: 'git.example.invalid' })).toBe(
    '[acme/widgets#5](https://git.example.invalid/acme/widgets/pull/5)',
  )
  expect(run('acme/widgets#5', false, { ...ctx, githubHost: 'bad host/' })).toBe('acme/widgets#5')
})

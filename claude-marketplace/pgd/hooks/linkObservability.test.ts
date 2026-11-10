// Tests of the link counters and the first-decline debug line, through the real rewrite with the
// real kinds and real strings.
import { expect, test } from 'claude-code/testing'
import { registerFileRefKind } from './fileRef'
import { registerGithubPrKind } from './githubPr'
import { registerJiraKind } from './jira'
import { resetLinkDeclineLog, resetLinkMemo, rewriteLinks, type LinkContext, type LinkRule } from './links'
import { initLog, flushLog } from './log'
import { METRIC_NAMES, resetMetrics, snapshot } from './metrics'
import { stubHost } from './testkit'

registerFileRefKind()
registerGithubPrKind()
registerJiraKind()

const files: LinkRule = { kind: 'file-ref', name: 'files' } as LinkRule
const prs: LinkRule = { kind: 'github-pr', name: 'prs', options: { sessionRepoFallback: false } } as LinkRule
const jira: LinkRule = {
  kind: 'jira',
  name: 'tracker',
  options: { baseUrl: 'https://tracker.example/browse', keys: ['ABC'] },
} as LinkRule
const ctx: LinkContext = { cwd: '/work/proj', home: '/home/u', githubOwners: ['acme'], knownRepos: ['acme/widgets'] }

const fresh = async (): Promise<ReturnType<typeof stubHost>> => {
  await flushLog() // lines a previous test queued must land before the buffer is reset
  resetLinkMemo()
  resetLinkDeclineLog()
  resetMetrics()
  const stub = stubHost()
  initLog(stub.host, () => '2026-01-01T00:00:00.000Z')
  return stub
}
const count = (name: string, labels: Record<string, string>): number =>
  snapshot().find(s => s.name === name && JSON.stringify(s.labels) === JSON.stringify(labels))?.value ?? 0
const logged = async (stub: ReturnType<typeof stubHost>): Promise<string[]> => {
  await flushLog()
  const last = stub.writes.filter(w => w.path.endsWith('.log')).pop()
  return last ? last.text.split('\n').filter(l => l !== '') : []
}

test('the counter names are registered', () => {
  expect(METRIC_NAMES.includes('pgd_links_resolved_total')).toBe(true)
  expect(METRIC_NAMES.includes('pgd_links_declined_total')).toBe(true)
})

test('a resolution counts once on a miss and not on a hit', async () => {
  await fresh()
  const labels = { kind: 'jira', rule: 'tracker' }
  expect(rewriteLinks('see ABC-12', [jira], ctx)).toBe('see [ABC-12](https://tracker.example/browse/ABC-12)')
  expect(count('pgd_links_resolved_total', labels)).toBe(1)
  rewriteLinks('see ABC-12', [jira], ctx) // memo hit
  expect(count('pgd_links_resolved_total', labels)).toBe(1)
  rewriteLinks('see ABC-12 and ABC-13', [jira], ctx) // a new text is a miss; two links
  expect(count('pgd_links_resolved_total', labels)).toBe(3)
})

const reasons: Array<[string, LinkRule, string, LinkContext | undefined]> = [
  ['no-context', files, 'see foo.go:42', undefined],
  ['no-repo', prs, 'see PR 12', ctx],
  ['unknown-repo', prs, 'see other/thing#3', ctx],
  ['adjacent', jira, 'see a/ABC-12', ctx],
  ['unsafe', files, 'run setup.sh:3', ctx],
]
for (const [reason, rule, text, context] of reasons) {
  test(`decline reason ${reason} is reachable with the real kind and counted once per miss`, async () => {
    await fresh()
    const labels = { kind: rule.kind as string, rule: rule.name, reason }
    expect(rewriteLinks(text, [rule], context)).toBe(text)
    expect(count('pgd_links_declined_total', labels)).toBe(1)
    expect(count('pgd_links_resolved_total', { kind: rule.kind as string, rule: rule.name })).toBe(0)
    if (reason !== 'no-context') {
      rewriteLinks(text, [rule], context) // a declined message is memoized: a hit counts nothing
      expect(count('pgd_links_declined_total', labels)).toBe(1)
    }
  })
}

test('the label set stays bounded: only kind, rule and the five reasons appear', async () => {
  await fresh()
  for (const [, rule, text, context] of reasons) rewriteLinks(text, [rule], context)
  rewriteLinks('ABC-1 and ABC-2 and ABC-3', [jira], ctx)
  const allowed = ['no-context', 'no-repo', 'unknown-repo', 'adjacent', 'unsafe']
  for (const s of snapshot()) {
    expect(['pgd_links_resolved_total', 'pgd_links_declined_total'].includes(s.name)).toBe(true)
    expect(['file-ref', 'github-pr', 'jira'].includes(s.labels.kind)).toBe(true)
    expect(['files', 'prs', 'tracker'].includes(s.labels.rule)).toBe(true)
    if (s.name === 'pgd_links_declined_total') expect(allowed.includes(s.labels.reason)).toBe(true)
    else expect(Object.keys(s.labels).sort()).toEqual(['kind', 'rule'])
  }
})

test('one debug line per (kind, reason), with no message text, and none per link', async () => {
  const stub = await fresh()
  const secret = 'zzsecretword'
  rewriteLinks(`${secret} a/ABC-1`, [jira], ctx)
  rewriteLinks(`${secret} b/ABC-2`, [jira], ctx) // same (kind, reason): silent
  rewriteLinks(`${secret} c/ABC-3 ABC-4`, [jira], ctx)
  rewriteLinks(`${secret} PR 5`, [prs], ctx) // another kind and reason: a second line
  const lines = (await logged(stub)).map(l => JSON.parse(l))
  expect(lines.map(l => l.message)).toEqual([
    'link declined: kind=jira reason=adjacent',
    'link declined: kind=github-pr reason=no-repo',
  ])
  for (const l of lines) {
    expect(l.level).toBe('debug')
    expect(JSON.stringify(l).includes(secret)).toBe(false)
  }
  resetLinkDeclineLog()
  resetLinkMemo()
  rewriteLinks('d/ABC-5', [jira], ctx)
  expect((await logged(stub)).length).toBe(3)
})

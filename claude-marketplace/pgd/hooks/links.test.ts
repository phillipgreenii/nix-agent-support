// Tests of the transcript links: the pure rewrite, the render hook and its registration.
import { expect, test } from 'claude-code/testing'
import { linkHook, registerLinkHooks, rewriteLinks, type LinkRule } from './links'
import { setOptions } from './options'
import { initLog, flushLog } from './log'
import { stubHost } from './testkit'
import { DEFAULT_OPTIONS, parseOptions, type PgdOptions } from './types'

const jira: LinkRule = { name: 'jira', pattern: '\\bZR-\\d+\\b', url: 'https://example.invalid/browse/{0}' }

test('a match becomes a markdown link whose url is the template with {0} replaced', () => {
  expect(rewriteLinks('see ZR-12 now', [jira])).toBe('see [ZR-12](https://example.invalid/browse/ZR-12) now')
  expect(rewriteLinks('ZR-1 and ZR-2', [jira])).toBe(
    '[ZR-1](https://example.invalid/browse/ZR-1) and [ZR-2](https://example.invalid/browse/ZR-2)',
  )
  expect(rewriteLinks('nothing here', [jira])).toBe('nothing here')
  expect(rewriteLinks('ZR-1', [])).toBe('ZR-1')
})

test('rules apply in list order and a span an earlier rule rewrote is not matched again', () => {
  const rules: LinkRule[] = [
    { name: 'a', pattern: 'ZR-\\d+', url: 'https://a.invalid/{0}' },
    { name: 'b', pattern: '\\d+', url: 'https://b.invalid/{0}' },
    { name: 'c', pattern: 'ZR', url: 'https://c.invalid/{0}' },
  ]
  expect(rewriteLinks('ZR-7 and 42', rules)).toBe('[ZR-7](https://a.invalid/ZR-7) and [42](https://b.invalid/42)')
  // swapping the order swaps who wins the shared span
  expect(rewriteLinks('ZR-7', [rules[1], rules[0]])).toBe('ZR-[7](https://b.invalid/7)')
  // the url of an earlier rewrite is not scanned by a later rule
  expect(rewriteLinks('ZR-7', [rules[0], { name: 'd', pattern: 'a\\.invalid', url: 'https://d.invalid/{0}' }])).toBe(
    '[ZR-7](https://a.invalid/ZR-7)',
  )
})

test('no rewrite inside an inline code span or a fenced code block', () => {
  expect(rewriteLinks('use `ZR-1` here', [jira])).toBe('use `ZR-1` here')
  expect(rewriteLinks('use ``a ` ZR-1`` here', [jira])).toBe('use ``a ` ZR-1`` here')
  const fenced = 'before ZR-1\n```ts\nconst a = "ZR-2"\n```\nafter ZR-3'
  expect(rewriteLinks(fenced, [jira])).toBe(
    'before [ZR-1](https://example.invalid/browse/ZR-1)\n```ts\nconst a = "ZR-2"\n```\nafter [ZR-3](https://example.invalid/browse/ZR-3)',
  )
  const tilde = '~~~\nZR-9\n~~~\nZR-8'
  expect(rewriteLinks(tilde, [jira])).toBe('~~~\nZR-9\n~~~\n[ZR-8](https://example.invalid/browse/ZR-8)')
  // an unclosed fence runs to the end of the text
  expect(rewriteLinks('```\nZR-4', [jira])).toBe('```\nZR-4')
})

test('the match is percent-encoded in the url and escaped in the link text', () => {
  const r: LinkRule = { name: 'p', pattern: 'a b\\]c', url: 'https://example.invalid/?q={0}' }
  expect(rewriteLinks('x a b]c y', [r])).toBe('x [a b\\]c](https://example.invalid/?q=a%20b%5Dc) y')
})

test('an invalid regex or an invalid template skips that rule only', () => {
  const bad: LinkRule[] = [
    { name: 'regex', pattern: '(', url: 'https://x.invalid/{0}' },
    { name: 'no-placeholder', pattern: 'ZR-\\d+', url: 'https://x.invalid/fixed' },
    { name: 'not-a-url', pattern: 'ZR-\\d+', url: 'not a url {0}' },
  ]
  expect(rewriteLinks('ZR-1', bad)).toBe('ZR-1')
  expect(rewriteLinks('ZR-1', [...bad, jira])).toBe('[ZR-1](https://example.invalid/browse/ZR-1)')
})

test('an empty match is skipped, and text holding a placeholder character is left alone', () => {
  expect(rewriteLinks('abc', [{ name: 'e', pattern: 'x*', url: 'https://x.invalid/{0}' }])).toBe('abc')
  const odd = 'ZR-1 '
  expect(rewriteLinks(odd, [jira])).toBe(odd)
})

test('a pattern that spans a code span does not lose the code span', () => {
  const r: LinkRule = { name: 's', pattern: 'a `b` c', url: 'https://x.invalid/{0}' }
  // the code span is protected first, so the pattern cannot see its backticks: no match, text intact
  expect(rewriteLinks('a `b` c', [r])).toBe('a `b` c')
})

test('the shipped default link list holds no bead entry (it is empty)', () => {
  expect(DEFAULT_OPTIONS.links).toEqual([])
  expect(parseOptions(undefined).links).toEqual([])
  expect(parseOptions('{}').links.some(l => /bead|\bbd\b/i.test(`${l.name}${l.pattern}${l.url}`))).toBe(false)
})

const optionsWith = (links: boolean): PgdOptions => ({
  ...DEFAULT_OPTIONS,
  components: { ...DEFAULT_OPTIONS.components, links },
  links: [jira],
})

test('the hook rewrites props.text only, on a copy, and makes no host call', () => {
  setOptions(optionsWith(true))
  const stub = stubHost()
  const e = { component: 'AssistantMessage', props: { text: 'fix ZR-5', isSummary: false } }
  const seen: unknown[] = []
  linkHook(stub.host, e, (x: unknown) => seen.push(x))
  expect(seen).toEqual([
    { component: 'AssistantMessage', props: { text: 'fix [ZR-5](https://example.invalid/browse/ZR-5)', isSummary: false } },
  ])
  expect(e.props.text).toBe('fix ZR-5') // the event handed in is untouched
  expect(stub.calls).toEqual([]) // nothing but the drawing changes: no process, file, fetch or config call
})

test('with components.links off or enable false the hook hands the event on untouched', () => {
  const e = { component: 'AssistantMessage', props: { text: 'fix ZR-5' } }
  for (const o of [optionsWith(false), { ...optionsWith(true), enable: false }]) {
    setOptions(o)
    const seen: unknown[] = []
    linkHook(undefined, e, (x: unknown) => seen.push(x))
    expect(seen).toEqual([e])
    expect(seen[0]).toBe(e)
  }
})

test('the hook is registered on the AssistantMessage render site', () => {
  const seen: Array<{ name: string; sel: unknown; fn: unknown }> = []
  registerLinkHooks((name: string, sel: unknown, fn: unknown) => seen.push({ name, sel, fn }))
  expect(seen.map(s => [s.name, s.sel])).toEqual([['ui.render', { component: 'AssistantMessage' }]])
  expect(seen[0].fn).toBe(linkHook)
})

test('a skipped rule is logged', async () => {
  const stub = stubHost()
  initLog(stub.host)
  rewriteLinks('ZR-1', [{ name: 'broken', pattern: '(', url: 'https://x.invalid/{0}' }])
  await flushLog()
  expect(stub.writes.some(w => w.text.includes('link rule \\"broken\\" skipped'))).toBe(true)
})

// Tests of the transcript links: the pure rewrite, the render hook and its registration.
import { expect, test } from 'claude-code/testing'
import {
  isDecline,
  linkHook,
  registerKind,
  registerLinkHooks,
  resetLinkMemo,
  rewriteLinks,
  type LinkRule,
} from './links'
import { setOptions } from './options'
import { initLog, flushLog } from './log'
import { stubHost } from './testkit'
import { DEFAULT_OPTIONS, parseOptions, type PgdOptions } from './types'

const jira: LinkRule = { name: 'jira', pattern: '\\bABC-\\d+\\b', url: 'https://example.invalid/browse/{0}' }

test('a match becomes a markdown link whose url is the template with {0} replaced', () => {
  expect(rewriteLinks('see ABC-12 now', [jira])).toBe('see [ABC-12](https://example.invalid/browse/ABC-12) now')
  expect(rewriteLinks('ABC-1 and ABC-2', [jira])).toBe(
    '[ABC-1](https://example.invalid/browse/ABC-1) and [ABC-2](https://example.invalid/browse/ABC-2)',
  )
  expect(rewriteLinks('nothing here', [jira])).toBe('nothing here')
  expect(rewriteLinks('ABC-1', [])).toBe('ABC-1')
})

test('rules apply in list order and a span an earlier rule rewrote is not matched again', () => {
  const rules: LinkRule[] = [
    { name: 'a', pattern: 'ABC-\\d+', url: 'https://a.invalid/{0}' },
    { name: 'b', pattern: '\\d+', url: 'https://b.invalid/{0}' },
    { name: 'c', pattern: 'ABC', url: 'https://c.invalid/{0}' },
  ]
  expect(rewriteLinks('ABC-7 and 42', rules)).toBe('[ABC-7](https://a.invalid/ABC-7) and [42](https://b.invalid/42)')
  // swapping the order swaps who wins the shared span
  expect(rewriteLinks('ABC-7', [rules[1], rules[0]])).toBe('ABC-[7](https://b.invalid/7)')
  // the url of an earlier rewrite is not scanned by a later rule
  expect(rewriteLinks('ABC-7', [rules[0], { name: 'd', pattern: 'a\\.invalid', url: 'https://d.invalid/{0}' }])).toBe(
    '[ABC-7](https://a.invalid/ABC-7)',
  )
})

test('no rewrite inside an inline code span or a fenced code block', () => {
  expect(rewriteLinks('use `ABC-1` here', [jira])).toBe('use `ABC-1` here')
  expect(rewriteLinks('use ``a ` ABC-1`` here', [jira])).toBe('use ``a ` ABC-1`` here')
  const fenced = 'before ABC-1\n```ts\nconst a = "ABC-2"\n```\nafter ABC-3'
  expect(rewriteLinks(fenced, [jira])).toBe(
    'before [ABC-1](https://example.invalid/browse/ABC-1)\n```ts\nconst a = "ABC-2"\n```\nafter [ABC-3](https://example.invalid/browse/ABC-3)',
  )
  const tilde = '~~~\nABC-9\n~~~\nABC-8'
  expect(rewriteLinks(tilde, [jira])).toBe('~~~\nABC-9\n~~~\n[ABC-8](https://example.invalid/browse/ABC-8)')
  // an unclosed fence runs to the end of the text
  expect(rewriteLinks('```\nABC-4', [jira])).toBe('```\nABC-4')
})

test('the match is percent-encoded in the url and escaped in the link text', () => {
  const r: LinkRule = { name: 'p', pattern: 'a b\\]c', url: 'https://example.invalid/?q={0}' }
  expect(rewriteLinks('x a b]c y', [r])).toBe('x [a b\\]c](https://example.invalid/?q=a%20b%5Dc) y')
})

test('an invalid regex or an invalid template skips that rule only', () => {
  const bad: LinkRule[] = [
    { name: 'regex', pattern: '(', url: 'https://x.invalid/{0}' },
    { name: 'no-placeholder', pattern: 'ABC-\\d+', url: 'https://x.invalid/fixed' },
    { name: 'not-a-url', pattern: 'ABC-\\d+', url: 'not a url {0}' },
  ]
  expect(rewriteLinks('ABC-1', bad)).toBe('ABC-1')
  expect(rewriteLinks('ABC-1', [...bad, jira])).toBe('[ABC-1](https://example.invalid/browse/ABC-1)')
})

test('an empty match is skipped, and text holding a placeholder character is left alone', () => {
  expect(rewriteLinks('abc', [{ name: 'e', pattern: 'x*', url: 'https://x.invalid/{0}' }])).toBe('abc')
  const odd = 'ABC-1 '
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
  const e = { component: 'AssistantMessage', props: { text: 'fix ABC-5', isSummary: false } }
  const seen: unknown[] = []
  linkHook(stub.host, e, (x: unknown) => seen.push(x))
  expect(seen).toEqual([
    { component: 'AssistantMessage', props: { text: 'fix [ABC-5](https://example.invalid/browse/ABC-5)', isSummary: false } },
  ])
  expect(e.props.text).toBe('fix ABC-5') // the event handed in is untouched
  expect(stub.calls).toEqual([]) // nothing but the drawing changes: no process, file, fetch or config call
})

test('with components.links off or enable false the hook hands the event on untouched', () => {
  const e = { component: 'AssistantMessage', props: { text: 'fix ABC-5' } }
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
  rewriteLinks('ABC-1', [{ name: 'broken', pattern: '(', url: 'https://x.invalid/{0}' }])
  await flushLog()
  expect(stub.writes.some(w => w.text.includes('link rule \\"broken\\" skipped'))).toBe(true)
})

// ---- engine: protected spans, escaping, kind dispatch, memo --------------------------------

const digits: LinkRule = { name: 'digits', pattern: '\\d+', url: 'https://example.com/{0}' }
const key: LinkRule = { name: 'key', pattern: 'ABC-\\d+', url: 'https://example.com/browse/{0}' }

test('existing markdown links, autolinks and bare urls are never rewritten', () => {
  resetLinkMemo()
  expect(rewriteLinks('[ABC-1](https://h/x)', [key])).toBe('[ABC-1](https://h/x)')
  expect(rewriteLinks('see [the ABC-1 note](https://h/x) ok', [key])).toBe('see [the ABC-1 note](https://h/x) ok')
  expect(rewriteLinks('https://h/foo/bar#12', [{ name: 'h', pattern: '#\\d+', url: 'https://example.com/{0}' }])).toBe(
    'https://h/foo/bar#12',
  )
  expect(rewriteLinks('go <https://h/a/12> now', [digits])).toBe('go <https://h/a/12> now')
  expect(rewriteLinks('ABC-1 https://h/x/ABC-2', [key])).toBe('[ABC-1](https://example.com/browse/ABC-1) https://h/x/ABC-2')
})

test('link text escapes underscore, star and angle bracket (regex rule only at this stage)', () => {
  resetLinkMemo()
  // only a regex rule exists in the engine at this stage, so the escaping is pinned through one
  const r: LinkRule = { name: 'py', pattern: '\\*\\*init\\*\\*\\.py:\\d+', url: 'https://example.com/?q={0}' }
  expect(rewriteLinks('x **init**.py:12 y', [r])).toBe('x [\\*\\*init\\*\\*.py:12](https://example.com/?q=**init**.py%3A12) y')
  const u: LinkRule = { name: 'u', pattern: '__init__\\.py:\\d+', url: 'https://example.com/?q={0}' }
  expect(rewriteLinks('__init__.py:3', [u])).toBe('[\\_\\_init\\_\\_.py:3](https://example.com/?q=__init__.py%3A3)')
  const a: LinkRule = { name: 'a', pattern: '<x>', url: 'https://example.com/?q={0}' }
  expect(rewriteLinks('a <x> b', [a])).toBe('a [\\<x>](https://example.com/?q=%3Cx%3E) b')
})

test('a kind registered with scan and resolve dispatches, and a decline leaves the text plain', () => {
  resetLinkMemo()
  registerKind('probe', (rule: { name: string }) => ({
    scan: t => [...t.matchAll(/\bP\d\b/g)].map(m => ({ start: m.index!, end: m.index! + 2, text: m[0] })),
    resolve: (c, _t, ctx) =>
      c.text === 'P1' ? { decline: 'adjacent' as const } : ctx?.cwd ? `https://example.com/${ctx.cwd}/${c.text}` : { decline: 'no-context' as const },
  }))
  const rules = [{ kind: 'probe', name: 'p' }] as unknown as LinkRule[]
  expect(isDecline({ decline: 'unsafe' })).toBe(true)
  expect(isDecline('https://example.com/')).toBe(false)
  expect(rewriteLinks('P1 P2', rules, { cwd: 'w' })).toBe('P1 [P2](https://example.com/w/P2)')
  expect(rewriteLinks('P1 P3', rules)).toBe('P1 P3') // no context: declined, not stored
})

test('the memo: a linked message is never re-resolved, a no-context message is not stored', () => {
  resetLinkMemo()
  let calls = 0
  registerKind('counting', () => ({
    scan: t => [...t.matchAll(/\bC\d\b/g)].map(m => ({ start: m.index!, end: m.index! + 2, text: m[0] })),
    resolve: (c, _t, ctx) => {
      calls++
      return c.text === 'C9' && !ctx?.cwd ? { decline: 'no-context' as const } : `https://example.com/${c.text}`
    },
  }))
  const rules = [{ kind: 'counting', name: 'c' }] as unknown as LinkRule[]
  expect(rewriteLinks('C1', rules, { cwd: 'a' })).toBe('[C1](https://example.com/C1)')
  expect(rewriteLinks('C1', rules, { cwd: 'b' })).toBe('[C1](https://example.com/C1)')
  expect(calls).toBe(1) // the second draw, with a different snapshot, came from the memo
  // mixed outcome: one candidate declined with no-context, so nothing is stored
  expect(rewriteLinks('C1 C9', rules)).toBe('[C1](https://example.com/C1) C9')
  const before = calls
  expect(rewriteLinks('C1 C9', rules, { cwd: 'a' })).toBe('[C1](https://example.com/C1) [C9](https://example.com/C9)')
  expect(calls).toBe(before + 2) // re-resolved in full once the context arrived
  expect(rewriteLinks('C1 C9', rules)).toBe('[C1](https://example.com/C1) [C9](https://example.com/C9)')
  expect(calls).toBe(before + 2) // and now it is final
})

test('the memo holds 64 messages, least recently used goes first', () => {
  resetLinkMemo()
  let calls = 0
  registerKind('lru', () => ({
    scan: t => [...t.matchAll(/L\d+/g)].map(m => ({ start: m.index!, end: m.index! + m[0].length, text: m[0] })),
    resolve: c => {
      calls++
      return `https://example.com/${c.text}`
    },
  }))
  const rules = [{ kind: 'lru', name: 'l' }] as unknown as LinkRule[]
  for (let i = 0; i < 64; i++) rewriteLinks(`L${i}`, rules)
  expect(calls).toBe(64)
  rewriteLinks('L0', rules) // touch the oldest: L1 is now the least recently used
  expect(calls).toBe(64)
  rewriteLinks('L64', rules) // evicts L1
  expect(calls).toBe(65)
  rewriteLinks('L0', rules)
  expect(calls).toBe(65)
  rewriteLinks('L1', rules)
  expect(calls).toBe(66)
})

test('an invalid rule is logged once per rule set, not on every call', async () => {
  resetLinkMemo()
  const stub = stubHost()
  initLog(stub.host)
  const rules: LinkRule[] = [{ name: 'broken-once', pattern: '(', url: 'https://x.invalid/{0}' }, jira]
  for (let i = 0; i < 3; i++) rewriteLinks(`ABC-${i}`, rules)
  await flushLog()
  expect(stub.writes.filter(w => w.text.includes('broken-once')).length).toBe(1)
})

test('an unknown kind is skipped alone', () => {
  resetLinkMemo()
  const rules = [{ kind: 'nope', name: 'n' }, jira] as unknown as LinkRule[]
  expect(rewriteLinks('ABC-1', rules)).toBe('[ABC-1](https://example.invalid/browse/ABC-1)')
})

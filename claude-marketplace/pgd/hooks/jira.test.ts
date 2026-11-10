// Tests of the jira link kind, through the real rewrite with real strings.
import { expect, test } from 'claude-code/testing'
import { registerJiraKind } from './jira'
import { resetLinkMemo, rewriteLinks, type LinkRule } from './links'

registerJiraKind()

const rule = (keys: string[]): LinkRule => ({
  kind: 'jira',
  name: 'jira',
  options: { baseUrl: 'https://tracker.example/browse', keys },
})
const run = (text: string, keys: string[] = ['ABC', 'DEF']): string => {
  resetLinkMemo()
  return rewriteLinks(text, [rule(keys)])
}
const link = (key: string): string => `[${key}](https://tracker.example/browse/${key})`

test('allowlisted keys link to baseUrl plus the match', () => {
  expect(run('fix ABC-12 now')).toBe(`fix ${link('ABC-12')} now`)
  expect(run('see ABC-12.')).toBe(`see ${link('ABC-12')}.`)
  expect(run('DEF-3, ABC-4')).toBe(`${link('DEF-3')}, ${link('ABC-4')}`)
})

test('a slash after a complete key links both keys', () => {
  expect(run('ABC-1/ABC-2')).toBe(`${link('ABC-1')}/${link('ABC-2')}`)
})

test('text that is not an allowlisted key stays plain', () => {
  for (const s of ['UTF-8', 'SHA-256', 'abc-12', 'ABCD-12', 'XABC-12', 'ABC-12x', 'ABC-', 'ABC 12']) {
    expect(run(s)).toBe(s)
  }
})

test('a key in a path stays plain', () => {
  for (const s of ['src/ABC-12.go:5', 'a/ABC-12', 'a\\ABC-12', 'ABC-12.go', 'ABC-12:5']) {
    expect(run(s)).toBe(s)
  }
})

test('regex metacharacters in keys are escaped', () => {
  expect(run('AXB-1 and A.B-1', ['A.B'])).toBe(`AXB-1 and ${link('A.B-1')}`)
})

test('an empty allowlist links nothing', () => {
  expect(run('ABC-12', [])).toBe('ABC-12')
})

// Tests of the mermaid fence claimer: closed fences get a link, everything else is untouched.
import { expect, test } from 'claude-code/testing'
import { appendMermaidLinks, mermaidFileUrl } from './mermaid'

const url = (s: string): string => `file:///r/${s.length}.svg`
const link = (s: string): string => `\n\n[mermaid diagram (rendered file)](${url(s)})`

test('a closed backtick mermaid fence keeps its source and gets a link appended', () => {
  const fence = '```mermaid\ngraph TD\n  A --> B\n```'
  expect(appendMermaidLinks(`before\n${fence}\nafter`, url)).toBe(`before\n${fence}${link(fence)}\nafter`)
  expect(appendMermaidLinks(fence, url)).toBe(fence + link(fence))
})

test('a closed tilde mermaid fence is claimed too', () => {
  const fence = '~~~mermaid\ngraph TD\n~~~'
  expect(appendMermaidLinks(fence, url)).toBe(fence + link(fence))
})

test('an open (unterminated) fence is left byte-for-byte', () => {
  for (const t of ['```mermaid\ngraph TD\n  A --> B', '```mermaid\n', '```mer', '~~~mermaid\ngraph TD\n', 'x\n```mermaid\ngraph']) {
    expect(appendMermaidLinks(t, url)).toBe(t)
  }
})

test('a four-space indented fence is indented code and is left byte-for-byte', () => {
  const t = 'text\n\n    ```mermaid\n    graph TD\n    ```\n'
  expect(appendMermaidLinks(t, url)).toBe(t)
})

test('other languages and a shorter closing run are not claimed', () => {
  const js = '```js\nmermaid\n```'
  expect(appendMermaidLinks(js, url)).toBe(js)
  const shortClose = '````mermaid\ngraph\n```'
  expect(appendMermaidLinks(shortClose, url)).toBe(shortClose)
})

test('two closed fences each get their own link, and the default url is a file url', () => {
  const a = '```mermaid\na\n```'
  const b = '```mermaid\nbb\n```'
  expect(appendMermaidLinks(`${a}\n${b}`, url)).toBe(`${a}${link(a)}\n${b}${link(b)}`)
  expect(mermaidFileUrl(a, '/tmp/x y').startsWith('file:///tmp/x%20y/')).toBe(true)
})

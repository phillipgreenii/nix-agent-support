import { tmpdir } from 'node:os'
import { FENCE_CLOSED } from './links'

// Mermaid fences in the transcript: a render pass claims CLOSED mermaid fences only, keeps the
// source visible and appends a link to an out-of-band rendered file (the renderer is separate;
// this pass only names where the file is expected). An open fence (still streaming) is left
// byte-for-byte, so a half-written diagram is never claimed. The fence definition is the one the
// protected-span pass in links.ts uses (FENCE_CLOSED), so the two cannot disagree.
const MERMAID_INFO = /^ {0,3}(`{3,}|~{3,})[ \t]*mermaid(?:[ \t{][^\n]*)?\n/i
const CLOSING_RUN = /(`{3,}|~{3,})[`~]*[ \t]*$/

// 32-bit FNV-1a of the fence source, as 8 hex digits: a stable file name without node:crypto.
export function mermaidHash(source: string): string {
  let h = 0x811c9dc5
  for (let i = 0; i < source.length; i++) {
    h ^= source.charCodeAt(i)
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return h.toString(16).padStart(8, '0')
}

// Where the renderer is expected to put the file for one fence source.
export function mermaidFileUrl(source: string, dir: string = `${tmpdir()}/pgd-mermaid`): string {
  return `file://${encodeURI(dir)}/${mermaidHash(source)}.svg`
}

export function appendMermaidLinks(text: string, urlFor: (source: string) => string = mermaidFileUrl): string {
  if (typeof text !== 'string' || !/mermaid/i.test(text)) return text
  return text.replace(FENCE_CLOSED, m => {
    const head = MERMAID_INFO.exec(m)
    if (!head) return m
    // the shared fence regex may back off an opening run (a four-backtick opener closed by three);
    // claim only when the closing run is at least as long as the opening one
    const close = CLOSING_RUN.exec(m.slice(head[0].length - 1))
    if (!close || close[0].trimEnd().length < head[1].length) return m
    return `${m}\n\n[mermaid diagram (rendered file)](${urlFor(m)})`
  })
}

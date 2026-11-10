import { getOptions } from './options'
import { log } from './log'
import type { PgdLink, PgdOptions } from './types'

// Transcript links: a render hook on assistant-message text rewrites matches of the configured
// link rules into markdown links. A rewrite changes only what is drawn: the hook hands next() a
// copy of the event whose props.text is rewritten, and touches nothing else (no host call, no
// stored state). The choices the design left to the builder are listed in README.md.
export type LinkRule = PgdLink

// Placeholders: a protected span (a code span, a fenced block or a span an earlier rule already
// rewrote) is replaced by ONE private-use character during the pass, so no later rule can match
// inside it and a pattern such as `\d+` never sees digits of a placeholder. The pass is skipped
// for text that already holds a private-use character from the range, and stops protecting after
// MAX_SPANS spans.
const BASE = 0xe000
const MAX_SPANS = 0xf8ff - BASE
const PLACEHOLDER = /[-]/g

// Fenced code blocks (CommonMark: a line of at least three backticks or tildes, indented at most
// three spaces, up to a closing fence of the same character and at least the same length, or the
// end of the text), then inline code spans (a backtick run closed by a run of equal length).
const FENCE = /^( {0,3})(`{3,}|~{3,})[^\n]*\n(?:[\s\S]*?^ {0,3}\2[`~]*[ \t]*$|[\s\S]*)/gm
const CODE_SPAN = /(`+)(?!`)[\s\S]*?[^`]\1(?!`)|(`+)(?!`)[\s\S]*?\2(?!`)/g

// A rule compiled once: the regular expression (flags `g`; no `u`, `i` or `m`), the url template.
interface Compiled {
  rule: LinkRule
  re: RegExp
}

function compile(rule: LinkRule): Compiled | undefined {
  // only regex rules are handled here; the other kinds have their own scanners
  if (rule.kind !== undefined && rule.kind !== 'regex') return undefined
  try {
    if (!rule.url.includes('{0}')) throw new Error('url template has no {0}')
    new URL(rule.url.split('{0}').join('x')) // throws on a template that is not a valid URL
    return { rule, re: new RegExp(rule.pattern, 'g') }
  } catch (err) {
    log('warn', 'links', `link rule "${rule.name}" skipped: ${err instanceof Error ? err.message : String(err)}`)
    return undefined
  }
}

// Link text escapes the characters that would end or nest the markdown link; the url takes the
// match percent-encoded (encodeURIComponent), so a match never breaks out of the url either.
const escapeText = (s: string): string => s.replace(/[\\[\]`]/g, c => `\\${c}`)

export function rewriteLinks(text: string, rules: LinkRule[]): string {
  if (typeof text !== 'string' || text === '' || !Array.isArray(rules) || rules.length === 0) return text
  if (/[-]/.test(text)) return text
  const compiled = rules.map(compile).filter((c): c is Compiled => c !== undefined)
  if (compiled.length === 0) return text

  const spans: string[] = []
  const protect = (original: string): string => {
    if (spans.length >= MAX_SPANS) return original
    spans.push(original)
    return String.fromCharCode(BASE + spans.length - 1)
  }
  let work = text.replace(FENCE, m => protect(m)).replace(CODE_SPAN, m => protect(m))

  for (const { rule, re } of compiled) {
    re.lastIndex = 0
    work = work.replace(re, (m: string) => {
      if (m === '') return m
      // the match may span placeholders; the link text restores them first
      const shown = restore(m, spans)
      const url = rule.url.split('{0}').join(encodeURIComponent(shown))
      return protect(`[${escapeText(shown)}](${url})`)
    })
  }
  return restore(work, spans)
}

// Put every protected span back (a span may hold the placeholder of an earlier span).
function restore(s: string, spans: string[]): string {
  let out = s
  for (let i = 0; i < 4 && PLACEHOLDER.test(out); i++) {
    PLACEHOLDER.lastIndex = 0
    out = out.replace(PLACEHOLDER, c => spans[c.charCodeAt(0) - BASE] ?? c)
  }
  PLACEHOLDER.lastIndex = 0
  return out
}

// The render hook, registered at registration time (the engine takes hooks at load, before the
// options are known) and gated by the parsed options on every draw: it rewrites only while
// components.links and enable are both true, otherwise it hands the event on untouched.
export function linksActive(options: PgdOptions | undefined): options is PgdOptions {
  return options?.enable === true && options.components.links === true
}

export function linkHook(_$: unknown, e: any, next: (e: any) => unknown): unknown {
  const options = getOptions()
  const text = e?.props?.text
  if (!linksActive(options) || typeof text !== 'string' || options.links.length === 0) return next(e)
  const rewritten = rewriteLinks(text, options.links)
  return next(rewritten === text ? e : { ...e, props: { ...e.props, text: rewritten } })
}

export function registerLinkHooks(on: any): void {
  on('ui.render', { component: 'AssistantMessage' }, linkHook)
}

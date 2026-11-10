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

// Protected spans besides fences and code spans: existing markdown links, autolinks and bare URLs,
// so an assistant-written link or a URL holding `#12` is never nested or rewritten.
const MD_LINK = /\[(?:[^\]\\]|\\.)*\]\([^)\s]*\)/g
const AUTOLINK = /<https?:\/\/[^>\s]+>/g
const BARE_URL = /https?:\/\/\S+/g

// ---- the kind interface -------------------------------------------------------------------
// A rule kind provides `scan` (find candidates in the text) and `resolve` (a URL for one
// candidate, or a decline with a reason). Both are pure functions of their arguments.
export type DeclineReason = 'no-context' | 'no-repo' | 'unknown-repo' | 'adjacent' | 'unsafe'
export interface Decline {
  decline: DeclineReason
}
export const isDecline = (r: string | Decline): r is Decline => typeof r !== 'string'

// What a session knows, handed in by the caller; every field is optional (see the design).
export interface LinkContext {
  cwd?: string
  repoSlug?: string
  home?: string
  githubHost?: string
  githubOwners?: string[]
  knownRepos?: string[]
}

// A candidate: `start`/`end` index the text handed to scan; `text` is the link text (the engine
// restores protected spans inside it before resolve sees it); `data` is kind-private.
export interface Candidate {
  start: number
  end: number
  text: string
  data?: unknown
}

export interface LinkKind {
  scan(text: string): Candidate[]
  resolve(candidate: Candidate, text: string, context?: LinkContext): string | Decline
}

// A kind factory compiles one rule once; it throws on a rule problem (the engine logs one warn).
export type KindFactory = (rule: any) => LinkKind
const KINDS = new Map<string, KindFactory>()
export function registerKind(name: string, factory: KindFactory): void {
  KINDS.set(name, factory)
}

// The regex kind: flags `g` only, the whole match percent-encoded into `{0}`, empty matches skipped.
registerKind('regex', (rule: { pattern: string; url: string }) => {
  if (!rule.url.includes('{0}')) throw new Error('url template has no {0}')
  new URL(rule.url.split('{0}').join('x')) // throws on a template that is not a valid URL
  const re = new RegExp(rule.pattern, 'g')
  return {
    scan(text) {
      const out: Candidate[] = []
      for (const m of text.matchAll(re)) {
        if (m[0] === '') continue
        out.push({ start: m.index!, end: m.index! + m[0].length, text: m[0] })
      }
      return out
    },
    resolve: c => rule.url.split('{0}').join(encodeURIComponent(c.text)),
  }
})

interface Compiled {
  name: string
  kind: LinkKind
}

function compile(rule: LinkRule): Compiled | undefined {
  const kindName = rule.kind ?? 'regex'
  try {
    const factory = KINDS.get(kindName)
    if (!factory) throw new Error(`unknown kind "${kindName}"`)
    return { name: rule.name, kind: factory(rule) }
  } catch (err) {
    log('warn', 'links', `link rule "${rule.name}" skipped: ${err instanceof Error ? err.message : String(err)}`)
    return undefined
  }
}

// Rules compile ONCE per rule set (by identity, then by content), so a rule problem logs once.
interface RuleSet {
  key: string
  compiled: Compiled[]
}
let byIdentity = new WeakMap<LinkRule[], RuleSet>()
let byContent = new Map<string, RuleSet>()
const MAX_RULE_SETS = 8

function ruleSetOf(rules: LinkRule[]): RuleSet {
  const known = byIdentity.get(rules)
  if (known) return known
  const key = JSON.stringify(rules)
  let set = byContent.get(key)
  if (!set) {
    set = { key, compiled: rules.map(compile).filter((c): c is Compiled => c !== undefined) }
    if (byContent.size >= MAX_RULE_SETS) byContent.delete(byContent.keys().next().value as string)
    byContent.set(key, set)
  }
  byIdentity.set(rules, set)
  return set
}

// ---- the memo: a bounded LRU keyed by (text, rule set), never by a snapshot -----------------
const MEMO_SIZE = 64
let memo = new Map<string, string>()

// Forget every memoized message and compiled rule set (tests call this so order cannot matter).
export function resetLinkMemo(): void {
  memo = new Map()
  byIdentity = new WeakMap()
  byContent = new Map()
}

// Link text escapes the characters that would end, nest or restyle the markdown link; the url
// takes the match percent-encoded (encodeURIComponent), so a match never breaks out of the url.
const escapeText = (s: string): string => s.replace(/[\\[\]`_*<]/g, c => `\\${c}`)

export function rewriteLinks(text: string, rules: LinkRule[], context?: LinkContext): string {
  if (typeof text !== 'string' || text === '' || !Array.isArray(rules) || rules.length === 0) return text
  if (/[-]/.test(text)) return text
  const set = ruleSetOf(rules)
  if (set.compiled.length === 0) return text

  const memoKey = `${set.key}\u0000${text}`
  const hit = memo.get(memoKey)
  if (hit !== undefined) {
    memo.delete(memoKey)
    memo.set(memoKey, hit)
    return hit
  }
  const { result, noContext } = rewriteUncached(text, set.compiled, context)
  if (!noContext) {
    if (memo.size >= MEMO_SIZE) memo.delete(memo.keys().next().value as string)
    memo.set(memoKey, result)
  }
  return result
}

function rewriteUncached(
  text: string,
  compiled: Compiled[],
  context?: LinkContext,
): { result: string; noContext: boolean } {
  const spans: string[] = []
  const protect = (original: string): string => {
    if (spans.length >= MAX_SPANS) return original
    spans.push(original)
    return String.fromCharCode(BASE + spans.length - 1)
  }
  let work = text
    .replace(FENCE, m => protect(m))
    .replace(CODE_SPAN, m => protect(m))
    .replace(MD_LINK, m => protect(m))
    .replace(AUTOLINK, m => protect(m))
    .replace(BARE_URL, m => protect(m))

  let noContext = false
  for (const { kind } of compiled) {
    let out = ''
    let at = 0
    for (const c of kind.scan(work)) {
      if (c.start < at || c.end <= c.start) continue
      // the match may span placeholders; the link text restores them first
      const shown = { ...c, text: restore(work.slice(c.start, c.end), spans) }
      const r = kind.resolve(shown, work, context)
      if (isDecline(r)) {
        if (r.decline === 'no-context') noContext = true
        continue
      }
      out += work.slice(at, c.start) + protect(`[${escapeText(shown.text)}](${r})`)
      at = c.end
    }
    work = out + work.slice(at)
  }
  return { result: restore(work, spans), noContext }
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

// Tests of the phase 1 band: the pure layout, the level colours, and the render hook.
import { expect, test } from 'claude-code/testing'
import { bandHook, contextLevel, renderBand } from './band'
import { setOptions } from './options'
import { defineSource, initSources, setValue } from './source'
import { DEFAULT_OPTIONS, parseOptions } from './types'
import type { Usage } from './usage'
import { settle, stubHost } from './testkit'

const T = DEFAULT_OPTIONS.alerts.thresholds
const usage = (p?: number): Usage => ({ contextPercent: p, limits: [] })
const labels = (rows: ReturnType<typeof renderBand>) => rows.map(r => r.map(l => l.label))

test('context label is normal below 60, yellow from 60 and red from 80', () => {
  expect(contextLevel(59.4, T)).toBe('normal')
  expect(contextLevel(60, T)).toBe('yellow')
  expect(contextLevel(79, T)).toBe('yellow')
  expect(contextLevel(80, T)).toBe('red')
  const lv = (p: number) =>
    renderBand({ sessionId: 'abcdef123456', usage: usage(p) }, 80, T)[0].find(l => l.id === 'context')
  expect(lv(42)?.label).toBe('ctx 42%')
  expect(lv(42)?.level).toBe('normal')
  expect(lv(60)?.level).toBe('yellow')
  expect(lv(80)?.level).toBe('red')
})

test('context level follows the alerts.thresholds option', () => {
  const t = { contextWarn: 30, contextAlert: 50, limit: 80 }
  expect(contextLevel(29, t)).toBe('normal')
  expect(contextLevel(30, t)).toBe('yellow')
  expect(contextLevel(50, t)).toBe('red')
})

test('an item whose condition is false is not drawn and nothing reads "no X"', () => {
  const rows = renderBand({ sessionId: 'abcdef123456', model: 'sonnet' }, 80, T)
  expect(labels(rows)).toEqual([['abcdef12', 'sonnet']])
  expect(labels(renderBand({ sessionId: 'abcdef123456', model: 'sonnet', effort: 'high' }, 80, T))).toEqual([
    ['abcdef12', 'sonnet · high'],
  ])
  // usage present but no contextPercent: no context label
  expect(labels(renderBand({ sessionId: 's1', usage: usage(undefined) }, 80, T))).toEqual([['s1']])
  expect(labels(renderBand({ sessionId: '' }, 80, T))).toEqual([])
  const all = labels(renderBand({ sessionId: 'abcdef123456', model: 'm', usage: usage(10) }, 80, T)).flat()
  expect(all.some(l => /\bno\b/i.test(l))).toBe(false)
})

test('labels wrap when the next would pass the width; a wide label gets its own row', () => {
  const v = { sessionId: 'abcdef12', model: 'sonnet', effort: 'high', usage: usage(5) }
  expect(labels(renderBand(v, 80, T))).toEqual([['abcdef12', 'sonnet · high', 'ctx 5%']])
  expect(labels(renderBand(v, 22, T))).toEqual([['abcdef12', 'sonnet · high'], ['ctx 5%']])
  expect(labels(renderBand(v, 10, T))).toEqual([['abcdef12'], ['sonnet · high'], ['ctx 5%']])
})

// A stub $ whose ui.resolve yields plain-data constructors and which records every call.
function stub$(calls: string[]) {
  const el = (type: string) => (p: Record<string, unknown>) => ({ type, ...p })
  return {
    session: {
      id: async () => {
        calls.push('session.id')
        return 'abcdef1234567890'
      },
    },
    ui: {
      resolve: () => {
        calls.push('ui.resolve')
        return { Box: el('Box'), Text: el('Text'), Button: el('Button') }
      },
    },
  }
}

const flat = (n: any): any[] =>
  n === null || typeof n !== 'object'
    ? []
    : Array.isArray(n)
      ? n.flatMap(flat)
      : [n, ...flat(n.children)]

test('the band draw reads cached values only and starts no fetch, process or file read', async () => {
  const sh = stubHost()
  initSources(sh.host)
  defineSource<Usage>({ id: 'usage', intervalMs: 0 })
  defineSource<string>({ id: 'model', intervalMs: 0 })
  defineSource<string>({ id: 'effort', intervalMs: 0 })
  setOptions(parseOptions(undefined))
  setValue('usage', usage(85))
  setValue('model', 'sonnet')
  setValue('effort', 'high')
  const before = sh.calls.length
  const calls: string[] = []
  const tree = await bandHook(stub$(calls), { props: { bodyColumns: 80 } }, async () => undefined)
  expect(calls).toEqual(['session.id', 'ui.resolve'])
  // a redraw request from the setValue calls above is not a fetch, process or file read
  expect(sh.calls.slice(before).filter(c => c !== 'ui.invalidate')).toEqual([])
  // the session id, model and context labels are buttons whose child is the coloured Text
  const texts = flat(tree).filter(n => n.type === 'Text' && n.key !== undefined && !String(n.key).endsWith('-gap'))
  expect(texts.map(t => t.children)).toEqual(['abcdef12', 'sonnet · high', 'ctx 85%'])
  expect(texts[2].color).toBe('red')
  expect(texts[0].color).toBe(undefined)
  expect(flat(tree).filter(n => n.type === 'Button').map(b => b.key)).toEqual(['session', 'model', 'context'])
})

test('the hook awaits next(e) and nests its result so another plugin band row is kept', async () => {
  initSources(stubHost().host)
  defineSource<string>({ id: 'model', intervalMs: 0 })
  setOptions(parseOptions(undefined))
  setValue('model', 'sonnet')
  const other = { type: 'Text', children: 'other plugin band' }
  let nextCalled = 0
  const tree = await bandHook(
    stub$([]),
    { props: { bodyColumns: 80 } },
    async () => {
      nextCalled++
      return other
    },
  )
  expect(nextCalled).toBe(1)
  expect(flat(tree)).toContain(other)
  expect(flat(tree).some(n => n.children === 'sonnet')).toBe(true)
})

test('with nothing to draw, or display disabled, the answer from next(e) is returned untouched', async () => {
  initSources(stubHost().host)
  const other = { type: 'Text', children: 'x' }
  const next = async () => other
  setOptions(parseOptions(JSON.stringify({ enable: false })))
  expect(await bandHook(stub$([]), { props: {} }, next)).toBe(other)
  setOptions(parseOptions(undefined))
  const $ = stub$([])
  $.session.id = async () => ''
  expect(await bandHook($, { props: {} }, next)).toBe(other)
})

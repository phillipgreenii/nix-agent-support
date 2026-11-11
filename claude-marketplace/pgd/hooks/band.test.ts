// Tests of the phase 1 band: the pure layout, the level colours, and the render hook.
import { expect, test } from 'claude-code/testing'
import { bandHook, clearBandButtons, contextLevel, textStyle, drawnWidth, ENGINE_RESERVE, registerBandButton, renderBand } from './band'
import { setOptions } from './options'
import { defineSource, initSources, setValue } from './source'
import { DEFAULT_OPTIONS, parseOptions } from './types'
import type { Usage } from './usage'
import { settle, stubHost } from './testkit'

const T = DEFAULT_OPTIONS.alerts.thresholds
const usage = (p?: number): Usage => ({ contextPercent: p, limits: [] })
const labels = (rows: ReturnType<typeof renderBand>) => rows.map(r => r.map(l => l.label))

test('context label is green below 60, yellow from 60 and red from 80', () => {
  expect(contextLevel(59.4, T)).toBe('green')
  // always colour coded: no input, 0 included, gives the plain level
  expect(contextLevel(0, T)).toBe('green')
  expect(contextLevel(59.9, T)).toBe('green')
  expect(renderBand({ sessionId: 'abcdef123456', usage: usage(0) }, 80, T)[0].find(l => l.id === 'context')?.level).toBe('green')
  expect(renderBand({ sessionId: 'abcdef123456', usage: usage(59.9) }, 80, T)[0].find(l => l.id === 'context')?.level).toBe('green')
  expect(contextLevel(60, T)).toBe('yellow')
  expect(contextLevel(79, T)).toBe('yellow')
  expect(contextLevel(80, T)).toBe('red')
  const lv = (p: number) =>
    renderBand({ sessionId: 'abcdef123456', usage: usage(p) }, 80, T)[0].find(l => l.id === 'context')
  expect(lv(42)?.label).toBe('ctx 42%')
  expect(lv(42)?.level).toBe('green')
  expect(lv(60)?.level).toBe('yellow')
  expect(lv(80)?.level).toBe('red')
})

test('context level follows the alerts.thresholds option', () => {
  const t = { contextWarn: 30, contextAlert: 50, limit: 80 }
  expect(contextLevel(29, t)).toBe('green')

  const ctx = (tokens?: number) =>
    renderBand({ sessionId: 'abcdef123456', usage: { contextPercent: 10, contextTokens: tokens, limits: [] } }, 80, T)[0].find(
      l => l.id === 'context',
    )
  expect(ctx(199_999)?.label).toBe('ctx 10%')
  expect(ctx(199_999)?.level).toBe('green')
  expect(ctx(200_000)?.label).toBe('ctx 10% (!)')
  expect(ctx(200_000)?.level).toBe('red')
  expect(ctx(undefined)?.label).toBe('ctx 10%')
  const row = (id: string) =>
    renderBand({ sessionId: 'abcdef123456', sessionName: 'my-name', model: 'sonnet', effort: 'high' }, 80, T)
      .flat()
      .find(l => l.id === id)
  expect(row('model')?.level).toBe('cyan')
  expect(row('session')?.bold).toBe(true)
  expect(row('session')?.label).toBe('my-name')
  expect(textStyle('cyan')).toEqual({ color: 'cyan' })
  expect(textStyle(undefined, true)).toEqual({ bold: true })
  expect(textStyle('dim', true)).toEqual({ dimColor: true, bold: true })
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
  // drawn widths: session button 12, model text 13, ctx button 9; the engine control reserves 4
  expect(labels(renderBand(v, 80, T))).toEqual([['abcdef12', 'sonnet · high', 'ctx 5%']])
  expect(labels(renderBand(v, 30, T))).toEqual([['abcdef12', 'sonnet · high'], ['ctx 5%']])
  expect(labels(renderBand(v, 10, T))).toEqual([['abcdef12'], ['sonnet · high'], ['ctx 5%']])
})

// The bead's band: session name, model, Focus and Wrap up buttons (no toggle button: the engine draws the only [-]), as the real components
// register them (real labels, real button chrome).
describe_band_widths()
function describe_band_widths() {
  const NAMES = {
    default: 'verify-live-a2',
    long: 'vl-a-much-longer-session-name-for-wrapping',
  }
  const cases: [string, keyof typeof NAMES, number][] = [
    ['52 columns, default name', 'default', 52],
    ['56 columns, default name', 'default', 56],
    ['80 columns, long name', 'long', 80],
    ['80 columns, default name', 'default', 80],
    ['200 columns, default name', 'default', 200],
  ]
  for (const [title, name, width] of cases) {
    test(`band at ${title}: labels whole, every row clear of the engine control`, () => {
      registerBandButton({ id: 'focus', order: 60, label: () => 'Focus', action: 'focus' })
      registerBandButton({ id: 'wrapup', order: 70, label: () => 'Wrap up', action: 'wrapup' })
      const v = { sessionId: 'abcdef1234', sessionName: NAMES[name], model: 'claude-sonnet-5-5' }
      const rows = renderBand(v, width, T)
      const expected = ['session', 'model', 'focus', 'wrapup']
      // nothing dropped or split: every label appears once, whole, in order
      expect(rows.flat().map(l => l.id)).toEqual(expected)
      expect(rows.flat().find(l => l.id === 'wrapup')?.label).toBe('Wrap up')
      for (const row of rows) {
        const drawn = row.reduce((n, l, i) => n + drawnWidth(l) + (i ? 1 : 0), 0)
        // a row shares its width with the engine control unless one label alone is wider
        if (row.length > 1) expect(drawn + ENGINE_RESERVE <= width).toBe(true)
      }
      if (width === 200) expect(rows.length).toBe(1)
      if (width === 52) expect(rows.length > 1).toBe(true)
      clearBandButtons()
    })
  }
}

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

// Tests of the rate-limit capture: a usage reading in, the status.jsonl line pa-monitor reads out.
import { expect, test } from 'claude-code/testing'
import { buildRecord, captureLimits, flushLimitsCapture, initLimitsCapture, projectSlug } from './limits-capture'
import { stubHost } from './testkit'
import type { Usage } from './usage'

const T = Date.UTC(2026, 10, 9, 12, 0, 0)
const FIVE = '2026-11-09T15:00:00Z'
const FIVE_EPOCH = Date.UTC(2026, 10, 9, 15, 0, 0) / 1000
const SEVEN = '2026-11-14T00:00:00Z'
const SEVEN_EPOCH = Date.UTC(2026, 10, 14, 0, 0, 0) / 1000

const usage = (five: number, seven = 3): Usage => ({
  limits: [
    { kind: 'five_hour', usedPercent: five, resetsAt: FIVE },
    { kind: 'seven_day', usedPercent: seven, resetsAt: SEVEN },
  ],
})

function boot(now = T) {
  const s = stubHost()
  s.state.cwd = '/work/my_repo.1'
  s.state.id = 'abc-123'
  s.state.claudeConfigDir = '/cfg'
  const clock = { t: now }
  initLimitsCapture(s.host, () => clock.t)
  return { s, clock }
}

const PATH = '/cfg/projects/-work-my-repo-1/abc-123.status.jsonl'

test('the slug turns every non-alphanumeric character into a dash', () => {
  expect(projectSlug('/Users/a/b_c.d')).toBe('-Users-a-b-c-d')
})

test('a usage reading becomes the status.jsonl line the limit reader parses', async () => {
  const { s } = boot()
  captureLimits(usage(12.5))
  await flushLimitsCapture()
  expect(s.writes).toEqual([
    {
      path: PATH,
      text: `${JSON.stringify({
        ts: T / 1000,
        session_id: 'abc-123',
        five_hour_pct: 12.5,
        five_hour_resets_at: FIVE_EPOCH,
        seven_day_pct: 3,
        seven_day_resets_at: SEVEN_EPOCH,
      })}\n`,
    },
  ])
})

test('an unchanged reading adds no record, a changed one appends and keeps the earlier lines', async () => {
  const { s, clock } = boot()
  captureLimits(usage(10))
  clock.t += 60_000
  captureLimits(usage(10))
  clock.t += 60_000
  captureLimits(usage(11))
  await flushLimitsCapture()
  expect(s.writes).toHaveLength(2)
  const lines = s.writes[1].text.trim().split('\n').map(l => JSON.parse(l))
  expect(lines.map(l => l.five_hour_pct)).toEqual([10, 11])
  expect(lines.map(l => l.ts)).toEqual([T / 1000, T / 1000 + 120])
})

test('a resumed session keeps the lines already in its file', async () => {
  const { s } = boot()
  s.files.set(PATH, '{"ts":1,"session_id":"abc-123","five_hour_pct":1}\n')
  captureLimits(usage(2))
  await flushLimitsCapture()
  expect(s.writes[0].text.split('\n')[0]).toBe('{"ts":1,"session_id":"abc-123","five_hour_pct":1}')
  expect(s.writes[0].text.trim().split('\n')).toHaveLength(2)
})

test('an out-of-range percentage and a bad reset read as absent, never as a value', () => {
  const r = buildRecord(
    {
      limits: [
        { kind: 'five_hour', usedPercent: 1782958200, resetsAt: 'garbage' },
        { kind: 'seven_day', usedPercent: 40 },
      ],
    },
    's',
    5,
  )
  expect(r).toEqual({ ts: 5, session_id: 's', seven_day_pct: 40 })
})

test('a reading with no limit values writes nothing', async () => {
  const { s } = boot()
  captureLimits({ limits: [], contextPercent: 5 })
  await flushLimitsCapture()
  expect(s.writes).toHaveLength(0)
})

test('without CLAUDE_CONFIG_DIR the file goes under HOME/.claude, and a failed write throws nothing', async () => {
  const { s } = boot()
  s.state.claudeConfigDir = undefined
  captureLimits(usage(1))
  await flushLimitsCapture()
  expect(s.writes[0].path).toBe('/home/u/.claude/projects/-work-my-repo-1/abc-123.status.jsonl')
  s.host.fs.write = async () => {
    throw new Error('denied')
  }
  captureLimits(usage(2))
  await flushLimitsCapture()
})

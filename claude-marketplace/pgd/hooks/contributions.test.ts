// Tests of the contribution store: ordering, replacement, ttl expiry with a fake clock and the
// redraw requests.
import { expect, test } from 'claude-code/testing'
import { initContributions, listContributions, putContribution, removeContribution } from './contributions'
import { fakeClock } from './testkit'

function setup() {
  const clock = fakeClock()
  let invalidations = 0
  initContributions({ now: clock.now, setTimer: clock.setTimer, requestInvalidate: () => void invalidations++ })
  return { clock, count: () => invalidations }
}
const alert = (id: string, priority: number, extra: object = {}) => ({
  id,
  surface: 'alert' as const,
  priority,
  text: `t-${id}`,
  ...extra,
})

test('listing orders by priority, larger first, then by time added', () => {
  setup()
  putContribution(alert('low', 1))
  putContribution(alert('hi-a', 9))
  putContribution(alert('hi-b', 9))
  putContribution(alert('mid', 5))
  expect(listContributions('alert').map(c => c.id)).toEqual(['hi-a', 'hi-b', 'mid', 'low'])
})

test('surfaces are listed separately', () => {
  setup()
  putContribution(alert('a', 1))
  putContribution({ id: 'f', surface: 'focus', priority: 1, text: 'x', url: 'https://e.test' })
  expect(listContributions('alert').map(c => c.id)).toEqual(['a'])
  expect(listContributions('focus').map(c => c.id)).toEqual(['f'])
  expect(listContributions('focus')[0].url).toBe('https://e.test')
})

test('putting the same id replaces the contribution', () => {
  setup()
  putContribution(alert('a', 1))
  putContribution(alert('a', 7, { text: 'new' }))
  const l = listContributions('alert')
  expect(l).toHaveLength(1)
  expect(l[0].text).toBe('new')
  expect(l[0].priority).toBe(7)
})

test('remove drops a contribution and an unknown id is harmless', () => {
  setup()
  putContribution(alert('a', 1))
  removeContribution('a')
  removeContribution('never-there')
  expect(listContributions('alert')).toEqual([])
})

test('a contribution with ttlMs expires on its own and the display is invalidated', async () => {
  const { clock, count } = setup()
  putContribution(alert('a', 1, { ttlMs: 1000 }))
  putContribution(alert('keep', 2))
  const before = count()
  await clock.advance(999)
  expect(listContributions('alert').map(c => c.id)).toEqual(['keep', 'a'])
  await clock.advance(1)
  expect(listContributions('alert').map(c => c.id)).toEqual(['keep'])
  expect(count()).toBe(before + 1)
  expect(clock.pending()).toBe(0)
})

test('a contribution that stops refreshing disappears and a refresh restarts the ttl', async () => {
  const { clock } = setup()
  putContribution(alert('a', 1, { ttlMs: 1000 }))
  await clock.advance(800)
  putContribution(alert('a', 1, { ttlMs: 1000 }))
  await clock.advance(800)
  expect(listContributions('alert')).toHaveLength(1)
  await clock.advance(200)
  expect(listContributions('alert')).toEqual([])
})

test('without ttlMs a contribution never expires', async () => {
  const { clock } = setup()
  putContribution(alert('a', 1))
  await clock.advance(10 * 24 * 3600 * 1000)
  expect(listContributions('alert')).toHaveLength(1)
  expect(clock.pending()).toBe(0)
})

test('put and remove each request an invalidation', () => {
  const { count } = setup()
  putContribution(alert('a', 1))
  expect(count()).toBe(1)
  removeContribution('a')
  expect(count()).toBe(2)
})

test('the timer follows the earliest expiry after a removal', async () => {
  const { clock } = setup()
  putContribution(alert('soon', 1, { ttlMs: 100 }))
  putContribution(alert('later', 1, { ttlMs: 500 }))
  removeContribution('soon')
  await clock.advance(100)
  expect(listContributions('alert').map(c => c.id)).toEqual(['later'])
  await clock.advance(400)
  expect(listContributions('alert')).toEqual([])
})

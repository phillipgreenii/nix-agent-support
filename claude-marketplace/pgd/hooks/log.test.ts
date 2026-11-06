import { expect, test } from 'claude-code/testing'
import { flushLog, initLog, log, LOG_CAP_BYTES } from './log'
import { stubHost } from './testkit'

test('a line is one JSON object with time, level, source, message and error kind', async () => {
  const stub = stubHost()
  initLog(stub.host, () => '2026-01-01T00:00:00.000Z')
  log('warn', 'usage', 'fetch failed', 'timeout')
  await flushLog()
  const w = stub.writes[stub.writes.length - 1]
  expect(w.path).toBe('/state/pgd/sess-1.log')
  expect(w.text.endsWith('\n')).toBe(true)
  expect(JSON.parse(w.text.trim())).toEqual({
    time: '2026-01-01T00:00:00.000Z',
    level: 'warn',
    source: 'usage',
    message: 'fetch failed',
    errorKind: 'timeout',
  })
})

test('XDG_STATE_HOME unset falls back to ~/.local/state', async () => {
  const stub = stubHost()
  stub.state.stateHome = undefined
  initLog(stub.host)
  log('info', 's', 'm')
  await flushLog()
  expect(stub.writes[0].path).toBe('/home/u/.local/state/pgd/sess-1.log')
})

test('the file rotates to .log.1 when it reaches the cap, replacing an older one', async () => {
  const stub = stubHost()
  initLog(stub.host)
  const big = 'x'.repeat(Math.floor(LOG_CAP_BYTES / 3))
  for (let i = 0; i < 7; i++) log('info', 's', big)
  await flushLog()
  const rotated = stub.writes.filter(w => w.path === '/state/pgd/sess-1.log.1')
  expect(rotated.length).toBe(3)
  const last = stub.writes.filter(w => w.path === '/state/pgd/sess-1.log').pop()!
  expect(last.text.length <= LOG_CAP_BYTES).toBe(true)
  expect(last.text.trim().split('\n').length).toBe(1)
})

test('logging reads no file and starts no process', async () => {
  const stub = stubHost()
  initLog(stub.host)
  log('debug', 's', 'm')
  await flushLog()
  expect(stub.calls.filter(c => c.startsWith('fs.read') || c === 'process.run')).toEqual([])
})

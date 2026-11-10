import { expect, test } from 'claude-code/testing'
import { callEndData, callInput, checkData, emit, isNormalEnd, SeenSet, type Env } from './tracker'

// Plain-function tests with a fake Env (no engine): argv, stdin and failure handling.
function fakeEnv(opts: { fail?: 'reject' | 'throw-sync' } = {}) {
  const runs: { argv: readonly string[]; stdin?: string }[] = []
  const env: Env = {
    sessionId: async () => 'sess-1',
    now: async () => 1700000000000,
    run: async (argv, init) => {
      runs.push({ argv, stdin: init?.stdin })
      if (opts.fail === 'reject') throw new Error('append failed')
      return { exitCode: 1 }
    },
  }
  return { env, runs }
}

test('emit passes the session id explicitly and hands data to the append command on stdin', async () => {
  const { env, runs } = fakeEnv()
  await emit(env, {
    kind: 'tracker.call_start',
    toolUseId: 'tu1',
    agentId: 'ag1',
    data: { tool: 'Bash', input: { command: 'ls' } },
  })
  expect(runs).toHaveLength(1)
  expect(runs[0].argv).toEqual([
    'claude-tool-tracker', 'append',
    '--session-id', 'sess-1',
    '--source', 'tracker',
    '--kind', 'tracker.call_start',
    '--ts', '1700000000000',
    '--tool-use-id', 'tu1',
    '--agent-id', 'ag1',
  ])
  expect(JSON.parse(runs[0].stdin ?? '')).toEqual({ tool: 'Bash', input: { command: 'ls' } })
})

test('emit without a tool use id (session_end) omits the flag and uses the known session id', async () => {
  const { env, runs } = fakeEnv()
  await emit(env, { kind: 'tracker.session_end', sessionId: 'ended', data: { reason: 'clear', normal: true } })
  expect(runs[0].argv).not.toContain('--tool-use-id')
  expect(runs[0].argv).toContain('ended')
})

test('a failing append (nonzero exit, rejection) never rejects into the caller', async () => {
  const bad = fakeEnv({ fail: 'reject' })
  await expect(emit(bad.env, { kind: 'tracker.check', toolUseId: 't', data: {} })).resolves.toBeUndefined()
  const broken: Env = {
    sessionId: () => Promise.reject(new Error('no id')),
    now: async () => 1,
    run: async () => ({}),
  }
  await expect(emit(broken, { kind: 'tracker.check', toolUseId: 't', data: {} })).resolves.toBeUndefined()
})

test('emit does not wait for the append: it returns a promise while the process is still running', async () => {
  let release: () => void = () => {}
  const gate = new Promise<void>(r => (release = r))
  let finished = false
  const env: Env = {
    sessionId: async () => 's',
    now: async () => 1,
    run: async () => { await gate; finished = true },
  }
  const p = emit(env, { kind: 'tracker.running', toolUseId: 't', data: {} })
  expect(finished).toBe(false)
  release()
  await p
  expect(finished).toBe(true)
})

test('data builders', () => {
  expect(callInput({ tool: 'Bash', tool_use_id: 'x', agentId: 'a', requestMeta: {}, command: 'ls' })).toEqual({ command: 'ls' })
  expect(callEndData({ deny: 'no' })).toEqual({ outcome: 'refused', error: 'no' })
  expect(callEndData({ isError: true, text: 'boom' })).toEqual({ outcome: 'error', error: 'boom' })
  expect(callEndData({ isError: true })).toEqual({ outcome: 'error' })
  expect(callEndData({})).toEqual({ outcome: 'ok' })
  expect(checkData({ decision: 'ask', hook: 'PreToolUse', reason: 'r' }, 'person')).toEqual({
    decision: 'ask', hook: 'PreToolUse', reason: 'r', approver: 'person',
  })
  expect(isNormalEnd('clear')).toBe(true)
  expect(isNormalEnd('other')).toBe(false)
})

test('SeenSet reports an id once and stays bounded', () => {
  const s = new SeenSet(2)
  expect(s.add('a')).toBe(true)
  expect(s.add('a')).toBe(false)
  s.add('b')
  s.add('c') // evicts a
  expect(s.add('a')).toBe(true)
})

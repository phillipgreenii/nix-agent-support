import { expect, test } from 'claude-code/testing'

// The module itself, through the engine's own chain.
test('the mod emits call_start, check and call_end through the append command', async ($, on) => {
  const argvs: string[][] = []
  on('process.run', (_, e) => {
    argvs.push([...e.argv])
    return { value: { exitCode: 0, stdout: '', stderr: '', isStdoutTruncated: false, isStderrTruncated: false } }
  })
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('session.id', () => ({ value: 'sess-9' }))
  on('clock.now', () => ({ value: 1700000000000 }))
  on('tool.check', () => ({ decision: 'allow' }))
  on('tool.call', () => ({ result: {} }))
  await $.session.start({ cwd: '/', surface: null, isInteractive: false })
  await $.tool.call({ tool: 'Bash', command: 'ls' })
  await $.tool.check({ tool: 'Bash', input: { command: 'ls' } })
  await new Promise(r => setTimeout(r, 50))
  const kinds = argvs.map(a => a[a.indexOf('--kind') + 1])
  expect(kinds).toContain('tracker.call_start')
  expect(kinds).toContain('tracker.call_end')
  for (const a of argvs) expect(a).toContain('--session-id')
})

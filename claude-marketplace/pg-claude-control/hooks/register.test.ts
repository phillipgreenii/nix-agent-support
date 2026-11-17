// Node tests of the control mod's register module: the listener-exit path of session.start, driven
// through a fake engine `$` (the real engine is not available under plain node).
// Run: node --experimental-strip-types --test (the nix check pg-claude-control-mod-node-tests).
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { register } from './register'

type Piece = { stream: 'stdout' | 'stderr'; text: string } | { code: number }

// run drives session.start with a fake engine whose listener child yields the given pieces per
// spawn (a spawn past the script hangs forever), and returns once `settle` ms pass quietly.
async function run(scripts: Piece[][]) {
  const handlers: Record<string, (...a: any[]) => any> = {}
  register(((name: string, fn: (...a: any[]) => any) => {
    handlers[name] = fn
  }) as any)
  let spawns = 0
  let sleeps = 0
  const rejections: unknown[] = []
  const onRej = (r: unknown) => rejections.push(r)
  process.on('unhandledRejection', onRej)
  const $ = {
    env: { get: async (k: string) => (k === 'PG_CLAUDE_CONTROL_STATE_DIR' ? '/tmp/pgcc-register-test' : undefined) },
    clock: { now: () => Date.now(), sleep: async () => void sleeps++ },
    fs: { read: async () => '', write: async () => undefined },
    process: {
      run: async () => ({ exitCode: 0 }),
      spawn: () => {
        const pieces = scripts[spawns++]
        return (async function* () {
          if (!pieces) await new Promise(() => {})
          else yield* pieces
        })()
      },
    },
    plugin: { root: '/nonexistent' },
    turn: { abort: async () => undefined },
    prompt: { submit: async () => undefined, read: async () => '' },
    command: { run: async () => undefined },
  }
  await handlers['session.start']($, { sessionId: 's' }, async (e: unknown) => e)
  await new Promise(r => setTimeout(r, 200))
  process.off('unhandledRejection', onRej)
  return { spawns, sleeps, rejections }
}

test('a listener that refuses the inbox directory is not respawned', async () => {
  const r = await run([[{ stream: 'stderr', text: 'inbox_dir_refused: /x\n' }, { code: 20 }]])
  assert.deepEqual(r.rejections, [])
  assert.equal(r.spawns, 1)
  assert.equal(r.sleeps, 0)
})

test('a listener that exits 20 without a stderr line is not respawned', async () => {
  const r = await run([[{ code: 20 }]])
  assert.deepEqual(r.rejections, [])
  assert.equal(r.spawns, 1)
  assert.equal(r.sleeps, 0)
})

test('a listener that exits otherwise is respawned after the pause', async () => {
  const r = await run([[{ code: 1 }]])
  assert.deepEqual(r.rejections, [])
  assert.equal(r.sleeps, 1)
  assert.equal(r.spawns, 2)
})

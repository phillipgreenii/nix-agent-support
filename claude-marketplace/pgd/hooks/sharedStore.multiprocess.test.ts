// Opt-in multi-process check of the shared store: several REAL headless `claude -p` sessions load a
// throwaway test mod and each write and read shared:<id> keys through $.store for 20 rounds. It is
// excluded from the unit gate (test-pgd-mod and `claude plugin test`) by the .multiprocess.test.ts
// suffix and runs only through scripts/run-pgd-multiprocess-check.sh (switch PGD_MULTIPROCESS=1).
//
// Throwaway environment: the sessions run with CLAUDE_CONFIG_DIR pointing at a fresh temporary
// directory, so the operator's real ~/.claude plugin store is never touched. Credentials are NOT
// copied from the real config: the sessions authenticate from the environment the operator passes
// in (for example ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN), and the test fails with the
// sessions' own output when they cannot.
//
// Definitions: every round a session writes { w, r } to the shared hot key and polls it every 20 ms
// for a while, recording every distinct { w, r } it sees. A lost write is a write no session ever
// saw before it was overwritten. Read-after-write latency of a write is the time from the writer's
// set returning to the first read, in another session, that returns that value. Times are
// performance.timeOrigin + performance.now() (monotonic-derived, comparable across processes on
// one host to about a millisecond). The test fails only if a store file is ever unparseable or a
// session reads nothing; it prints the lost-write count and the maximum latency.
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

const SESSIONS = Number(process.env.PGD_MP_SESSIONS ?? 4)
const ROUNDS = Number(process.env.PGD_MP_ROUNDS ?? 20)

const REGISTER = `
export const register = on => {
  on('session.start', async ($, e, next) => {
    const idx = await $.env.get('PGD_MP_INDEX')
    const out = await $.env.get('PGD_MP_OUT')
    const start = Number(await $.env.get('PGD_MP_START'))
    const rounds = Number(await $.env.get('PGD_MP_ROUNDS'))
    const clk = () => performance.timeOrigin + performance.now()
    const sleep = ms => new Promise(r => $.clock.after(ms, r))
    const writes = []
    const seen = {}
    const note = v => {
      if (!v || typeof v.w !== 'string') return
      const k = v.w + ':' + v.r
      if (seen[k] === undefined) seen[k] = clk()
    }
    while (clk() < start) await sleep(10)
    for (let r = 0; r < rounds; r++) {
      await $.store.set('shared:mp:hot', { value: { w: idx, r }, fetchedAt: Date.now() })
      writes.push({ w: idx, r, returnedAt: clk() })
      for (let i = 0; i < 6; i++) {
        const got = await $.store.get('shared:mp:hot')
        note(got && got.value)
        await sleep(20)
      }
    }
    await $.fs.write(out, JSON.stringify({ idx, writes, seen }))
    return next(e)
  })
}
`

interface Rec {
  idx: string
  writes: Array<{ w: string; r: number; returnedAt: number }>
  seen: Record<string, number>
}

test('several real headless sessions share keys through $.store', { timeout: 540_000 }, async () => {
  const root = mkdtempSync(join(tmpdir(), 'pgd-mp-'))
  const config = join(root, 'config')
  const mod = join(root, 'mod')
  const out = join(root, 'out')
  mkdirSync(join(mod, '.claude-plugin'), { recursive: true })
  mkdirSync(join(mod, 'hooks'), { recursive: true })
  mkdirSync(out, { recursive: true })
  mkdirSync(config, { recursive: true })
  writeFileSync(
    join(mod, '.claude-plugin', 'plugin.json'),
    JSON.stringify({ name: 'pgdmp', description: 'shared store multi-process test mod', version: '1.0.0', defaultEnabled: true }),
  )
  writeFileSync(join(mod, 'hooks', 'hooks.json'), JSON.stringify({ modules: ['./register.ts'] }))
  writeFileSync(join(mod, 'hooks', 'register.ts'), REGISTER)
  const start = performance.timeOrigin + performance.now() + 15_000 // time for every session to boot

  const storeDir = join(config, 'plugins', 'store')
  let unparseable = 0
  let probes = 0
  const probe = (): void => {
    if (!existsSync(storeDir)) return
    for (const f of readdirSync(storeDir)) {
      if (!f.endsWith('.json')) continue
      probes++
      try {
        JSON.parse(readFileSync(join(storeDir, f), 'utf8'))
      } catch (err) {
        if ((err as NodeJS.ErrnoException).code !== 'ENOENT') unparseable++
      }
    }
  }
  const timer = setInterval(probe, 5)
  const logs: string[] = []
  try {
    const runs = Array.from(
      { length: SESSIONS },
      (_, i) =>
        new Promise<number | null>(resolve => {
          const p = spawn(
            'claude',
            ['-p', 'Reply with the single word ok.', '--model', 'haiku', '--plugin-dir', mod, '--no-session-persistence'],
            {
              env: {
                ...process.env,
                CLAUDE_CONFIG_DIR: config,
                PGD_MP_INDEX: String(i),
                PGD_MP_OUT: join(out, `${i}.json`),
                PGD_MP_START: String(start),
                PGD_MP_ROUNDS: String(ROUNDS),
              },
              stdio: ['ignore', 'pipe', 'pipe'],
            },
          )
          p.stdout.on('data', (d: unknown) => logs.push(`[${i}] ${d}`))
          p.stderr.on('data', (d: unknown) => logs.push(`[${i}!] ${d}`))
          p.on('close', resolve)
        }),
    )
    await Promise.all(runs)
  } finally {
    clearInterval(timer)
  }
  probe()

  const recs: Rec[] = []
  for (let i = 0; i < SESSIONS; i++) {
    const f = join(out, `${i}.json`)
    if (existsSync(f)) recs.push(JSON.parse(readFileSync(f, 'utf8')) as Rec)
  }
  try {
    const allWrites = recs.flatMap(r => r.writes)
    let lost = 0
    let maxLatencyMs = 0
    for (const w of allWrites) {
      const key = `${w.w}:${w.r}`
      const all = recs.map(r => r.seen[key]).filter((t): t is number => t !== undefined)
      if (all.length === 0) {
        lost++
        continue
      }
      const others = recs
        .filter(r => r.idx !== w.w)
        .map(r => r.seen[key])
        .filter((t): t is number => t !== undefined)
      if (others.length > 0) maxLatencyMs = Math.max(maxLatencyMs, Math.min(...others) - w.returnedAt)
    }
    console.log(
      `pgd multiprocess: sessions=${SESSIONS} rounds=${ROUNDS} writes=${allWrites.length} lostWrites=${lost} ` +
        `maxReadAfterWriteLatencyMs=${maxLatencyMs.toFixed(1)} storeProbes=${probes} unparseable=${unparseable}`,
    )
    assert.equal(recs.length, SESSIONS, `every session reports (got ${recs.length}); logs:\n${logs.join('')}`)
    for (const r of recs) assert.ok(Object.keys(r.seen).length > 0, `session ${r.idx} read nothing`)
    assert.equal(unparseable, 0, 'a store file was unparseable')
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

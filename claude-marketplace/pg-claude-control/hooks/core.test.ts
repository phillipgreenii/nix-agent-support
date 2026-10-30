// Node tests of the control mod's core against the real file system and the real listener script.
// Run: node --experimental-strip-types --test (the nix check pg-claude-control-mod-node-tests).
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, readdirSync, writeFileSync, existsSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import {
  Controller,
  IdWindow,
  LineBuffer,
  RespawnCounter,
  REQUEST_LOG_LINES,
  parseAllow,
  parseRequest,
  resolveStateDir,
  utf8Len,
  type Io,
} from './core'

const here = dirname(fileURLToPath(import.meta.url))
const LISTEN = join(here, '..', 'scripts', 'listen.sh')

interface Calls {
  submits: string[]
  aborts: string[]
  commands: Array<[string, string]>
}

function realIo(calls: Calls, over: Partial<Io> = {}): Io {
  return {
    now: async () => Date.now(),
    readText: async p => readFileSync(p, 'utf8'),
    writeText: async (p, t) => {
      mkdirSync(dirname(p), { recursive: true })
      writeFileSync(p, t)
    },
    run: (argv, init) =>
      new Promise(res => {
        const c = spawn(argv[0], argv.slice(1), { stdio: ['pipe', 'ignore', 'ignore'] })
        c.on('close', code => res({ exitCode: code ?? 1 }))
        c.stdin.on('error', () => undefined)
        c.stdin.end(init?.stdin ?? '')
      }),
    submit: async t => void calls.submits.push(t),
    abort: async id => void calls.aborts.push(id),
    command: async (c, a) => {
      calls.commands.push([c, a])
      return {}
    },
    readPrompt: async () => ({ text: 'draft é', cursor: 7 }),
    ...over,
  }
}

function fresh(allow = ['exit', 'clear', 'compact']) {
  const dir = mkdtempSync(join(tmpdir(), 'pgcc-'))
  const calls: Calls = { submits: [], aborts: [], commands: [] }
  return { dir, calls, ctl: new Controller(dir, allow), io: realIo(calls) }
}

async function reqs(ctl: Controller) {
  await ctl.flush()
  return lines(ctl.requestsPath)
}

const lines = (p: string) =>
  (existsSync(p) ? readFileSync(p, 'utf8') : '')
    .split('\n')
    .filter(Boolean)
    .map(l => JSON.parse(l))

test('utf8Len counts bytes, not UTF-16 units', () => {
  assert.equal(utf8Len('abc'), 3)
  assert.equal(utf8Len('é'), 2)
  assert.equal(utf8Len('€'), 3)
  assert.equal(utf8Len('😀'), 4)
})

test('the line cap: 511 content bytes plus newline fit, 512 do not', () => {
  const ok = new LineBuffer().push('a'.repeat(511) + '\n')
  assert.equal(ok.lines.length, 1)
  assert.equal(ok.oversize, 0)
  const over = new LineBuffer().push('a'.repeat(512) + '\n')
  assert.equal(over.lines.length, 0)
  assert.equal(over.oversize, 1)
})

test('multibyte text is counted as bytes', () => {
  assert.equal(new LineBuffer().push('é'.repeat(255) + '\n').lines.length, 1) // 510 bytes
  assert.equal(new LineBuffer().push('é'.repeat(256) + '\n').oversize, 1) // 512 bytes + newline
})

test('an oversize line is discarded up to its newline, then the buffer recovers, across pieces', () => {
  const b = new LineBuffer()
  assert.deepEqual(b.push('x'.repeat(300)), { lines: [], oversize: 0 })
  assert.deepEqual(b.push('x'.repeat(300)), { lines: [], oversize: 0 }) // cap crossed: discarding
  assert.deepEqual(b.push('x'.repeat(5000) + '\nping\nrun'), { lines: ['ping'], oversize: 1 })
  assert.deepEqual(b.push(' exit\n'), { lines: ['run exit'], oversize: 0 })
})

test('parseRequest reads id, verb, argument, payload and reply; an unknown verb is rejected', () => {
  assert.deepEqual(parseRequest('id=t1 submit hello world payload=/p/x reply=/r/y'), {
    ok: true,
    req: { id: 't1', verb: 'submit', arg: 'hello world', payload: '/p/x', reply: '/r/y' },
  })
  assert.deepEqual(parseRequest('ping'), { ok: true, req: { id: undefined, verb: 'ping', arg: '', payload: undefined, reply: undefined } })
  const bad = parseRequest('id=z1 rm -rf /')
  assert.equal(bad.ok, false)
})

test('the id window drops a repeat of the last 256 ids and forgets older ones', () => {
  const w = new IdWindow()
  for (let i = 0; i < 256; i++) assert.equal(w.repeat(`i${i}`), false)
  assert.equal(w.repeat('i0'), true)
  assert.equal(w.repeat('i255'), true)
  assert.equal(w.repeat('new'), false) // pushes i0 out
  assert.equal(w.repeat('i0'), false)
})

test('run.allow: default, custom, and exit always stays', () => {
  assert.deepEqual(parseAllow(undefined), ['exit', 'clear', 'compact'])
  assert.deepEqual(parseAllow('compact, /clear'), ['compact', 'clear', 'exit'])
})

test('state directory resolution order', () => {
  assert.equal(resolveStateDir({ override: '/o', xdg: '/x', home: '/h' }), '/o')
  assert.equal(resolveStateDir({ xdg: '/x', home: '/h' }), '/x/pg-claude-control')
  assert.equal(resolveStateDir({ home: '/h' }), '/h/.local/state/pg-claude-control')
})

test('the respawn counter counts consecutive failures, gives up after 5 and resets after 60 s of running', () => {
  const r = new RespawnCounter()
  for (let i = 1; i <= 5; i++) {
    const d = r.onExit(100)
    assert.equal(d.giveUp, false)
    assert.equal(d.consecutive, i)
  }
  assert.equal(r.onExit(100).giveUp, true)
  const r2 = new RespawnCounter()
  for (let i = 0; i < 4; i++) r2.onExit(100)
  assert.equal(r2.onExit(60_000).consecutive, 1) // ran 60 s: the count restarts
  for (let i = 0; i < 4; i++) assert.equal(r2.onExit(100).giveUp, false)
  const d = r2.onExit(100)
  assert.equal(d.giveUp, true)
  assert.ok(d.pauseMs > r.onExit(60_000).pauseMs) // the pause grows
})

test('run: only the allowlist runs; others are refused and logged', async () => {
  const { ctl, io, calls, dir } = fresh()
  await ctl.handleLine(io, 'run clear')
  await ctl.handleLine(io, 'run compact focus on x')
  await ctl.handleLine(io, 'run /bin-rm')
  assert.deepEqual(calls.commands, [
    ['clear', ''],
    ['compact', 'focus on x'],
  ])
  assert.ok(lines(ctl.eventsPath).some(e => e.event === 'request_rejected'))
  assert.match((await reqs(ctl))[2].outcome, /^refused/)
  assert.ok(dir)
})

test('submit refuses text starting with /, and sends other text', async () => {
  const { ctl, io, calls } = fresh()
  await ctl.handleLine(io, 'submit /exit')
  await ctl.handleLine(io, 'submit hello')
  assert.deepEqual(calls.submits, ['hello'])
})

test('a long submit travels in a payload file the mod reads and deletes; foreign paths are rejected', async () => {
  const { ctl, io, calls, dir } = fresh()
  const p = join(dir, 'inbox', 'payloads', 'p1')
  mkdirSync(dirname(p), { recursive: true })
  writeFileSync(p, 'long text é'.repeat(100))
  await ctl.handleLine(io, `submit payload=${p}`)
  assert.equal(calls.submits[0], 'long text é'.repeat(100))
  assert.equal(existsSync(p), false)
  await ctl.handleLine(io, 'submit payload=/etc/hosts')
  assert.equal(calls.submits.length, 1)
})

test('interrupt answers "no turn" with none, aborts the tracked turn otherwise', async () => {
  const { ctl, io, calls, dir } = fresh()
  const reply = join(dir, 'r1.json')
  await ctl.handleLine(io, `interrupt reply=${reply}`)
  assert.deepEqual(JSON.parse(readFileSync(reply, 'utf8')), { ok: false, reason: 'no turn' })
  ctl.onTurnStart({ turnId: 'sub', agentId: 'a1' })
  assert.equal(ctl.runningTurn, undefined)
  ctl.onTurnStart({ turnId: 't9' })
  await ctl.handleLine(io, 'interrupt')
  assert.deepEqual(calls.aborts, ['t9'])
  ctl.onTurnComplete({ turnId: 't9' })
  assert.equal(ctl.runningTurn, undefined)
})

test('read and ping reply through a temporary name moved into place with mv -f', async () => {
  const { ctl, io, dir } = fresh()
  const reply = join(dir, 'reply', 'r.json')
  await ctl.handleLine(io, `id=r1 read reply=${reply}`)
  assert.deepEqual(JSON.parse(readFileSync(reply, 'utf8')), { ok: true, result: { text: 'draft é', cursor: 7 } })
  assert.equal(existsSync(`${reply}.tmp`), false)
  await ctl.handleLine(io, `ping reply=${reply}`)
  assert.equal(JSON.parse(readFileSync(reply, 'utf8')).result.pong, true)
})

test('a failed reply is logged as reply_failed', async () => {
  const { ctl, calls } = fresh()
  const base = realIo(calls)
  const io = realIo(calls, { run: (argv, init) => (argv[0] === 'mv' ? Promise.resolve({ exitCode: 1 }) : base.run(argv, init)) })
  await ctl.handleLine(io, 'ping reply=/nonexistent-dir-xyz/r.json')
  assert.equal(lines(ctl.eventsPath)[0].event, 'reply_failed')
})

test('a repeated id is dropped and logged as a duplicate', async () => {
  const { ctl, io, calls } = fresh()
  await ctl.handleLine(io, 'id=d1 submit once')
  await ctl.handleLine(io, 'id=d1 submit once')
  assert.deepEqual(calls.submits, ['once'])
  assert.equal((await reqs(ctl))[1].outcome, 'duplicate')
})

test('an unknown verb is ignored and logged', async () => {
  const { ctl, io, calls } = fresh()
  await ctl.handleLine(io, 'rm -rf /')
  assert.deepEqual(calls, { submits: [], aborts: [], commands: [] })
  const ev = lines(ctl.eventsPath)
  assert.equal(ev[0].event, 'request_rejected')
  assert.match(ev[0].reason, /unknown verb/)
})

test('requests.jsonl keeps the last 1,000 lines; the events log is separate and append-only', async () => {
  const { ctl, io } = fresh()
  await ctl.handleLine(io, 'bogus')
  for (let i = 0; i < REQUEST_LOG_LINES + 25; i++) await ctl.handleLine(io, `id=n${i} ping`)
  const rl = await reqs(ctl)
  assert.equal(rl.length, REQUEST_LOG_LINES)
  assert.equal(rl[rl.length - 1].id, `n${REQUEST_LOG_LINES + 24}`)
  assert.equal(lines(ctl.eventsPath).length, 1) // the rejection survives the window
})

test('the pause seam holds a request between steps', async () => {
  const { ctl, calls } = fresh()
  let release: () => void = () => undefined
  const held = new Promise<void>(r => (release = r))
  const points: string[] = []
  const io = realIo(calls, {
    pause: async p => {
      points.push(p)
      await held
    },
  })
  const done = ctl.handleLine(io, 'submit hi')
  await new Promise(r => setTimeout(r, 50))
  assert.deepEqual(calls.submits, [])
  release()
  await done
  assert.deepEqual(points, ['before:submit'])
  assert.deepEqual(calls.submits, ['hi'])
})

test('the end file is <state>/ends/<session>.<unix-ms>.json and matches the schema', async () => {
  const { ctl, io, dir } = fresh()
  const final = await ctl.writeEnd(io, 'sess-1', 'prompt_input_exit')
  const body = JSON.parse(readFileSync(final, 'utf8'))
  const schema = JSON.parse(readFileSync(join(here, '..', 'schema', 'session-end.schema.json'), 'utf8'))
  assert.match(final, /\/ends\/sess-1\.\d+\.json$/)
  assert.deepEqual(Object.keys(body).sort(), [...schema.required].sort())
  assert.ok(schema.properties.reason.enum.includes(body.reason))
  assert.deepEqual(schema.properties.reason.enum, ['prompt_input_exit', 'clear', 'other', 'resume', 'logout'])
  assert.deepEqual(readdirSync(join(dir, 'ends')), [final.split('/').pop()]) // no temp file left
  assert.equal(final.endsWith(`.${body.timeMs}.json`), true)
})

// The live trigger, without a real Claude: the real listener under the real bash, as a child of a
// throwaway process, driven through the FIFO and the real Controller.
test('live: ping, 512 and 513 byte lines, an unknown verb and a duplicate id; the FIFO goes with the parent', async () => {
  const { ctl, io, dir } = fresh()
  const parent = spawn('bash', ['-c', 'bash "$1" & wait', '_', LISTEN], {
    env: { ...process.env, PG_CLAUDE_CONTROL_STATE_DIR: dir },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  const work: Promise<void>[] = []
  parent.stdout.setEncoding('utf8')
  parent.stdout.on('data', (d: string) => work.push(...ctl.ingest(io, d)))
  const fifo = join(dir, 'inbox', `${parent.pid}.fifo`)
  try {
  for (let i = 0; i < 60 && !existsSync(fifo); i++) await new Promise(r => setTimeout(r, 100))
  assert.ok(existsSync(fifo))
  const w = (line: string) => writeFileSync(fifo, line)
  const mk = (total: number) => `id=L${total} ping ` + 'z'.repeat(total - 1 - `id=L${total} ping `.length) + '\n'
  assert.equal(utf8Len(mk(512)), 512)
  w('id=p1 ping\n')
  w(mk(512))
  w(mk(513))
  w('id=u1 frobnicate\n')
  w('id=p1 ping\n')
  for (let i = 0; i < 50 && (await reqs(ctl)).length < 5; i++) await new Promise(r => setTimeout(r, 100))
  await Promise.all(work)
  const outcomes = (await reqs(ctl)).map(r => `${r.id}:${r.verb}:${r.outcome}`)
  assert.ok(outcomes.includes('p1:ping:ok'))
  assert.ok(outcomes.includes('L512:ping:ok'))
  assert.ok(!outcomes.some(o => o.startsWith('L513:')))
  assert.ok(outcomes.includes('null:oversize:rejected'))
  assert.ok(outcomes.includes('u1:unknown:rejected'))
  assert.ok(outcomes.includes('p1:ping:duplicate'))
  process.kill(parent.pid as number, 'SIGKILL')
  await new Promise(r => parent.on('close', r))
  for (let i = 0; i < 30 && existsSync(fifo); i++) await new Promise(r => setTimeout(r, 100))
  assert.equal(existsSync(fifo), false)
  } finally {
    parent.kill('SIGKILL')
  }
})

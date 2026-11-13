// pg-claude-control core logic. Pure of the engine: everything outside goes through `Io`, so the
// node tests run this against the real file system. Module helpers are top-level functions (claude
// plugin validate refuses a `$` passed to a helper declared inside register()).

export const MAX_LINE_BYTES = 512
export const ID_WINDOW = 256
export const REQUEST_LOG_LINES = 1000
export const RESPAWN_MAX_TRIES = 5
export const RESPAWN_RESET_MS = 60_000
export const DEFAULT_ALLOW = ['exit', 'clear', 'compact']
export const VERBS = ['submit', 'interrupt', 'run', 'read', 'ping'] as const
export type Verb = (typeof VERBS)[number]
export const END_REASONS = ['prompt_input_exit', 'clear', 'other', 'resume', 'logout']

/** UTF-8 byte length of a JS string (the module has no Node, so no Buffer or TextEncoder). */
export function utf8Len(s: string): number {
  let n = 0
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c < 0x80) n += 1
    else if (c < 0x800) n += 2
    else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
      n += 4
      i++
    } else n += 3
  }
  return n
}

/** State directory: PG_CLAUDE_CONTROL_STATE_DIR, else $XDG_STATE_HOME/pg-claude-control, else
 * ~/.local/state/pg-claude-control. The caller reads the three variables (each a string literal). */
export function resolveStateDir(v: { override?: string; xdg?: string; home?: string }): string {
  if (v.override) return v.override
  if (v.xdg) return `${v.xdg}/pg-claude-control`
  return `${v.home ?? ''}/.local/state/pg-claude-control`
}

/** run.allow: comma-separated names in PG_CLAUDE_CONTROL_RUN_ALLOW; default exit,clear,compact.
 * `exit` always stays, because `stop` runs it. */
export function parseAllow(raw: string | undefined): string[] {
  const list = (raw ?? '')
    .split(',')
    .map(s => s.trim().replace(/^\//, ''))
    .filter(Boolean)
  const out = list.length > 0 ? list : [...DEFAULT_ALLOW]
  if (!out.includes('exit')) out.push('exit')
  return out
}

export interface LineResult {
  lines: string[]
  oversize: number
}

/** Splits a stream into lines of at most MAX_LINE_BYTES including the newline. A longer line is
 * discarded up to its newline without being buffered beyond the cap. */
export class LineBuffer {
  private buf = ''
  private discarding = false

  push(chunk: string): LineResult {
    const lines: string[] = []
    let oversize = 0
    for (const ch of chunk) {
      if (ch === '\n') {
        if (this.discarding) {
          this.discarding = false
          oversize++
        } else {
          lines.push(this.buf)
        }
        this.buf = ''
        continue
      }
      if (this.discarding) continue
      this.buf += ch
      // content bytes + the newline must fit in the cap
      if (utf8Len(this.buf) + 1 > MAX_LINE_BYTES) {
        this.discarding = true
        this.buf = ''
      }
    }
    return { lines, oversize }
  }
}

export interface Request {
  id?: string
  verb: Verb
  arg: string
  payload?: string
  reply?: string
}

export type Parsed = { ok: true; req: Request } | { ok: false; reason: string; id?: string }

/** `[id=<token>] <verb> [argument] [payload=<path>] [reply=<path>]`; verbs are a fixed table. */
export function parseRequest(line: string): Parsed {
  let rest = line.replace(/\r$/, '').trim()
  let id: string | undefined
  const m = /^id=(\S+)\s*/.exec(rest)
  if (m) {
    id = m[1]
    rest = rest.slice(m[0].length)
  }
  const sp = rest.search(/\s/)
  const verb = sp < 0 ? rest : rest.slice(0, sp)
  let arg = sp < 0 ? '' : rest.slice(sp + 1).trim()
  if (!(VERBS as readonly string[]).includes(verb)) {
    return { ok: false, reason: `unknown verb: ${verb.slice(0, 40)}`, id }
  }
  let payload: string | undefined
  let reply: string | undefined
  for (;;) {
    const t = /(?:^|\s)(payload|reply)=(\S+)$/.exec(arg)
    if (!t) break
    if (t[1] === 'payload') payload = t[2]
    else reply = t[2]
    arg = arg.slice(0, arg.length - t[0].length).trim()
  }
  return { ok: true, req: { id, verb: verb as Verb, arg, payload, reply } }
}

/** The last ID_WINDOW ids; a repeat is a duplicate. */
export class IdWindow {
  private ids: string[] = []
  private set = new Set<string>()

  /** True when `id` was already in the window; otherwise records it. */
  repeat(id: string): boolean {
    if (this.set.has(id)) return true
    this.ids.push(id)
    this.set.add(id)
    if (this.ids.length > ID_WINDOW) this.set.delete(this.ids.shift() as string)
    return false
  }
}

export interface RespawnDecision {
  giveUp: boolean
  pauseMs: number
  consecutive: number
}

/** Counts consecutive listener failures; a listener that ran RESPAWN_RESET_MS resets the count. */
export class RespawnCounter {
  private consecutive = 0

  onExit(ranMs: number): RespawnDecision {
    if (ranMs >= RESPAWN_RESET_MS) this.consecutive = 0
    this.consecutive++
    const pauseMs = Math.min(500 * 2 ** (this.consecutive - 1), 15_000)
    return { giveUp: this.consecutive > RESPAWN_MAX_TRIES, pauseMs, consecutive: this.consecutive }
  }
}

export interface Io {
  now: () => Promise<number>
  readText: (path: string) => Promise<string>
  writeText: (path: string, text: string) => Promise<void>
  run: (argv: readonly string[], init?: { stdin?: string; timeoutMs?: number }) => Promise<{ exitCode: number }>
  submit: (text: string) => Promise<void>
  abort: (turnId: string) => Promise<void>
  command: (command: string, args: string) => Promise<{ text?: string }>
  readPrompt: () => Promise<{ text: string; cursor: number }>
  /** Faults-style pause seam: a test blocks here to hold a request between steps. */
  pause?: (point: string) => Promise<void>
}

/** Reply JSON, fixed for the command packet (exit 21 is `ok: false` with the mod's reason). */
export type Reply = { ok: true; result?: Record<string, unknown> } | { ok: false; reason: string }

export class Controller {
  readonly ids = new IdWindow()
  readonly lines = new LineBuffer()
  readonly respawn = new RespawnCounter()
  private turnId: string | undefined
  private log: string[] = []
  private logLoaded = false
  private chain: Promise<unknown> = Promise.resolve()
  private flushing: Promise<void> | undefined
  private dirty = false
  private flushIo: Pick<Io, 'now' | 'readText' | 'writeText' | 'run'> | undefined

  readonly stateDir: string
  readonly allow: string[]

  constructor(stateDir: string, allow: string[]) {
    this.stateDir = stateDir
    this.allow = allow
  }

  get inboxDir(): string {
    return `${this.stateDir}/inbox`
  }
  get requestsPath(): string {
    return `${this.stateDir}/requests.jsonl`
  }
  get eventsPath(): string {
    return `${this.stateDir}/events.jsonl`
  }

  onTurnStart(e: { turnId: string; agentId?: string }): void {
    if (e.agentId === undefined) this.turnId = e.turnId
  }
  onTurnComplete(e: { turnId?: string; agentId?: string }): void {
    if (e.agentId === undefined && (e.turnId === undefined || e.turnId === this.turnId)) this.turnId = undefined
  }
  get runningTurn(): string | undefined {
    return this.turnId
  }

  /** Serializes file writes: $.fs.write corrupts files under concurrent calls. */
  private serial<T>(fn: () => Promise<T>): Promise<T> {
    const next = this.chain.then(fn, fn)
    this.chain = next.catch(() => undefined)
    return next
  }

  async event(io: Pick<Io, 'now' | 'run'>, name: string, detail: Record<string, unknown> = {}): Promise<void> {
    const ts = await io.now()
    const line = JSON.stringify({ ts, event: name, ...detail }) + '\n'
    await this.serial(() => io.run(['tee', '-a', this.eventsPath], { stdin: line, timeoutMs: 2000 }).then(() => undefined)).catch(
      () => undefined,
    )
  }

  /** Records a request in the 1,000-line window. The file is rewritten (temporary name, mv) by a
   * coalescing flush, so a burst costs one rewrite, not one per request; `flush()` waits for it. */
  async logRequest(io: Pick<Io, 'now' | 'readText' | 'writeText' | 'run'>, id: string | undefined, verb: string, outcome: string): Promise<void> {
    const ts = await io.now()
    if (!this.logLoaded) {
      this.logLoaded = true
      try {
        this.log = (await io.readText(this.requestsPath)).split('\n').filter(Boolean)
      } catch {
        this.log = []
      }
    }
    this.log.push(JSON.stringify({ ts, id: id ?? null, verb, outcome }))
    if (this.log.length > REQUEST_LOG_LINES) this.log = this.log.slice(-REQUEST_LOG_LINES)
    this.flushIo = io
    if (this.flushing) {
      this.dirty = true
      return
    }
    this.flushing = (async () => {
      do {
        this.dirty = false
        const w = this.flushIo
        if (!w) break
        const tmp = `${this.requestsPath}.tmp`
        try {
          await w.writeText(tmp, this.log.join('\n') + '\n')
          await w.run(['mv', '-f', tmp, this.requestsPath])
        } catch {
          // best effort; the next request rewrites the whole window
        }
      } while (this.dirty)
      this.flushing = undefined
    })()
  }

  /** Resolves once every scheduled rewrite of requests.jsonl has landed. */
  async flush(): Promise<void> {
    while (this.flushing) await this.flushing
  }

  /** Feeds a stdout piece from the listener; every complete line is handled (not awaited in order). */
  ingest(io: Io, chunk: string): Promise<void>[] {
    const { lines, oversize } = this.lines.push(chunk)
    const work: Promise<void>[] = []
    for (let i = 0; i < oversize; i++) {
      work.push(this.reject(io, undefined, 'line over the 512-byte cap', 'oversize'))
    }
    for (const l of lines) work.push(this.handleLine(io, l))
    return work
  }

  private async reject(io: Io, id: string | undefined, reason: string, verb: string): Promise<void> {
    await this.event(io, 'request_rejected', { id: id ?? null, reason })
    await this.logRequest(io, id, verb, 'rejected')
  }

  async handleLine(io: Io, line: string): Promise<void> {
    if (line.trim() === '') return
    const p = parseRequest(line)
    if (!p.ok) return this.reject(io, p.id, p.reason, 'unknown')
    const req = p.req
    if (req.id !== undefined && this.ids.repeat(req.id)) {
      await this.logRequest(io, req.id, req.verb, 'duplicate')
      return
    }
    let reply: Reply
    let outcome: string
    try {
      await io.pause?.(`before:${req.verb}`)
      const r = await this.execute(io, req)
      reply = r
      outcome = r.ok ? 'ok' : `refused: ${r.reason}`
    } catch (err) {
      reply = { ok: false, reason: `error: ${String(err)}` }
      outcome = reply.reason
    }
    if (req.reply) await this.writeReply(io, req.reply, reply)
    await this.logRequest(io, req.id, req.verb, outcome)
  }

  private async execute(io: Io, req: Request): Promise<Reply> {
    switch (req.verb) {
      case 'ping':
        return { ok: true, result: { pong: true, ts: await io.now() } }
      case 'read': {
        const box = await io.readPrompt()
        return { ok: true, result: { text: box.text, cursor: box.cursor } }
      }
      case 'interrupt': {
        if (this.turnId === undefined) return { ok: false, reason: 'no turn' }
        await io.abort(this.turnId)
        return { ok: true }
      }
      case 'run': {
        const sp = req.arg.search(/\s/)
        const name = (sp < 0 ? req.arg : req.arg.slice(0, sp)).replace(/^\//, '')
        const args = sp < 0 ? '' : req.arg.slice(sp + 1).trim()
        if (!name) return { ok: false, reason: 'run needs a command' }
        if (!this.allow.includes(name)) {
          await this.event(io, 'request_rejected', { id: req.id ?? null, reason: `run not allowed: ${name.slice(0, 40)}` })
          return { ok: false, reason: `command not in run.allow: ${name}` }
        }
        await io.command(name, args)
        return { ok: true }
      }
      case 'submit': {
        let text = req.arg
        if (req.payload) {
          const dir = `${this.inboxDir}/payloads/`
          if (!req.payload.startsWith(dir) || req.payload.includes('..')) {
            await this.event(io, 'request_rejected', { id: req.id ?? null, reason: 'payload outside payloads/' })
            return { ok: false, reason: 'payload outside the payloads directory' }
          }
          text = await io.readText(req.payload)
          await io.run(['rm', '-f', req.payload]).catch(() => undefined)
        }
        if (text.startsWith('/')) return { ok: false, reason: 'text starting with / is refused' }
        if (text === '') return { ok: false, reason: 'empty text' }
        void io.submit(text).catch(() => undefined)
        return { ok: true }
      }
    }
  }

  /** JSON to a temporary name, then mv -f ($.fs has no rename). */
  async writeReply(io: Io, path: string, reply: Reply): Promise<void> {
    const tmp = `${path}.tmp`
    try {
      await io.writeText(tmp, JSON.stringify(reply) + '\n')
      const r = await io.run(['mv', '-f', tmp, path])
      if (r.exitCode !== 0) throw new Error(`mv exit ${r.exitCode}`)
    } catch (err) {
      await this.event(io, 'reply_failed', { path, error: String(err) })
    }
  }

  /** session.end: <stateDir>/ends/<session-id>.<unix-ms>.json through a temporary name and mv. */
  async writeEnd(io: Pick<Io, 'now' | 'writeText' | 'run'>, sessionId: string, reason: string): Promise<string> {
    const timeMs = await io.now()
    const final = `${this.stateDir}/ends/${sessionId}.${timeMs}.json`
    const tmp = `${this.stateDir}/ends/.${sessionId}.${timeMs}.tmp`
    await io.writeText(tmp, JSON.stringify({ sessionId, reason, timeMs }) + '\n')
    await io.run(['mv', '-f', tmp, final])
    return final
  }
}

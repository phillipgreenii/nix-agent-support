// The tracker mod's logic: turns Claude Code mod events into `claude-tool-tracker append`
// invocations (event kinds: packages/claude-tool-tracker/docs/event-kinds.md).
//
// Rules this file keeps:
//  - It only observes. Every hook returns what `next(e)` resolved to, unchanged.
//  - It never writes an event file; each event is handed to the append command in a separate
//    process ($.fs.write corrupts files under concurrent calls).
//  - It passes the session id explicitly: a mod's child processes get no CLAUDE_CODE_SESSION_ID.
//  - Appending is fire-and-forget: nothing on the tool path waits for it, and a failing append
//    (rejection or nonzero exit) is swallowed. One process per event, so a crash loses at most
//    the events still in flight.

export const BINARY = 'claude-tool-tracker'

/** The slice of `$` this module uses. The engine refuses a module that binds or passes `$` itself,
 * so each hook builds this from closures that spell `$.noun.method(...)` at the call site. */
export interface Env {
  sessionId: () => Promise<string>
  now: () => Promise<number>
  run: (
    argv: readonly string[],
    init?: { stdin?: string; timeoutMs?: number },
  ) => Promise<unknown>
}

export interface Emit {
  kind: string
  data: Record<string, unknown>
  toolUseId?: string
  agentId?: string
  /** Known session id (session.end carries it); otherwise `$.session.id()`. */
  sessionId?: string
}

const APPEND_TIMEOUT_MS = 5000

/** Starts the append for one event and returns the promise of its completion; never rejects. */
export function emit(env: Env, ev: Emit): Promise<void> {
  // The clock read starts now, before the caller goes on, so ts is the event's own moment.
  const nowP = env.now()
  return (async () => {
    try {
      const sessionId = ev.sessionId ?? (await env.sessionId())
      const ts = await nowP
      const argv = [
        BINARY,
        'append',
        '--session-id',
        sessionId,
        '--source',
        'tracker',
        '--kind',
        ev.kind,
        '--ts',
        String(ts),
      ]
      if (ev.toolUseId) argv.push('--tool-use-id', ev.toolUseId)
      if (ev.agentId) argv.push('--agent-id', ev.agentId)
      // The exit code is ignored by design: a producer never lets append change a verdict.
      await env.run(argv, {
        stdin: JSON.stringify(ev.data),
        timeoutMs: APPEND_TIMEOUT_MS,
      })
    } catch {
      // Observing never fails a call.
    }
  })()
}

const RESERVED = new Set(['tool', 'tool_use_id', 'agentId', 'requestMeta', 'consent'])

export function callInput(e: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(e)) if (!RESERVED.has(k)) out[k] = v
  return out
}

function compact(o: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(o)) if (v !== undefined && v !== null) out[k] = v
  return out
}

export interface CallResultLike {
  deny?: string
  isError?: boolean
  text?: string
}

/** call_end data from what `next(e)` resolved to. */
export function callEndData(r: CallResultLike): Record<string, unknown> {
  if (typeof r.deny === 'string') return { outcome: 'refused', error: r.deny }
  if (r.isError === true) return compact({ outcome: 'error', error: r.text })
  return { outcome: 'ok' }
}

export interface CheckResultLike {
  decision: string
  hook?: string
  reason?: string
  rule?: string
  setting?: string
  plugin?: string
}

export function checkData(r: CheckResultLike, approver?: string): Record<string, unknown> {
  return compact({
    decision: r.decision,
    hook: r.hook,
    reason: r.reason,
    rule: r.rule,
    setting: r.setting,
    plugin: r.plugin,
    approver,
  })
}

/** A bounded set of tool_use_ids already reported running, so a redraw does not repeat it. */
export class SeenSet {
  private ids = new Set<string>()
  private max: number
  constructor(max = 2048) {
    this.max = max
  }
  /** True the first time `id` is added. */
  add(id: string): boolean {
    if (this.ids.has(id)) return false
    if (this.ids.size >= this.max) {
      const first = this.ids.values().next().value
      if (first !== undefined) this.ids.delete(first)
    }
    this.ids.add(id)
    return true
  }
}

export function isNormalEnd(reason: string): boolean {
  return reason !== 'other'
}

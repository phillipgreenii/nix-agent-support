import type { Register } from 'claude-code'
import {
  callEndData,
  callInput,
  checkData,
  emit,
  isNormalEnd,
  SeenSet,
  type Env,
} from './tracker'

// The tracker mod: observes tool calls and hands each event to `claude-tool-tracker append`.
// The engine refuses a module that binds or passes `$`, so every hook spells out its own Env.
export const register: Register = on => {
  const running = new SeenSet()
  let cwd: string | undefined

  on('session.start', ($, e, next) => {
    cwd = e.cwd
    return next(e)
  })

  on('tool.call', ($, e, next) => {
    const env: Env = {
      sessionId: () => $.session.id(),
      now: () => $.clock.now(),
      run: (argv, init) => $.process.run(argv, init),
    }
    const base = { toolUseId: e.tool_use_id, agentId: e.agentId }
    const rec = e as unknown as Record<string, unknown>
    void emit(env, {
      ...base,
      kind: 'tracker.call_start',
      data: { tool: e.tool, input: callInput(rec), ...(cwd ? { cwd } : {}) },
    })
    return next(e).then(
      r => {
        void emit(env, { ...base, kind: 'tracker.call_end', data: callEndData(r) })
        return r
      },
      (err: unknown) => {
        const interrupted = next.signal?.aborted === true
        void emit(env, {
          ...base,
          kind: 'tracker.call_end',
          data: interrupted
            ? { outcome: 'interrupted' }
            : { outcome: 'error', error: String(err) },
        })
        throw err
      },
    )
  })

  on('tool.check', async ($, e, next) => {
    const r = await next(e)
    if (e.tool_use_id) {
      void emit(
        {
          sessionId: () => $.session.id(),
          now: () => $.clock.now(),
          run: (argv, init) => $.process.run(argv, init),
        },
        {
          kind: 'tracker.check',
          toolUseId: e.tool_use_id,
          agentId: e.agentId,
          data: checkData(r, e.approver),
        },
      )
    }
    return r
  })

  on('ui.render', { component: 'ToolUse' }, ($, e, next) => {
    const p = e.props
    if (p.isRunning && running.add(p.tool_use_id)) {
      void emit(
        {
          sessionId: () => $.session.id(),
          now: () => $.clock.now(),
          run: (argv, init) => $.process.run(argv, init),
        },
        { kind: 'tracker.running', toolUseId: p.tool_use_id, data: { tool: p.tool } },
      )
    }
    return next(e)
  })

  on('session.end', async ($, e, next) => {
    // Bounded by the engine's session.end budget; the append is awaited so it is not cut off.
    await emit(
      {
        sessionId: () => $.session.id(),
        now: () => $.clock.now(),
        run: (argv, init) => $.process.run(argv, init),
      },
      {
        kind: 'tracker.session_end',
        sessionId: e.sessionId,
        data: { reason: e.reason, normal: isNormalEnd(e.reason) },
      },
    )
    return next(e)
  })
}

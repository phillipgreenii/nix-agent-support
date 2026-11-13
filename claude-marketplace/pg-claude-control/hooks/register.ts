import type { Register } from 'claude-code'
import { Controller, END_REASONS, parseAllow, resolveStateDir, type Io } from './core'

// The control mod: on session.start it spawns scripts/listen.sh (a child of the claude process,
// holding <claude pid>.fifo) and acts on the request lines it prints; on session.end it writes the
// end file. State directory: PG_CLAUDE_CONTROL_STATE_DIR, else $XDG_STATE_HOME/pg-claude-control,
// else ~/.local/state/pg-claude-control. run.allow: PG_CLAUDE_CONTROL_RUN_ALLOW (comma-separated,
// default exit,clear,compact). The engine refuses a module that binds or passes `$`, so each hook
// spells out its own closures.
export const register: Register = on => {
  let ctl: Controller | undefined

  on('turn.start', ($, e, next) => {
    ctl?.onTurnStart(e)
    return next(e)
  })

  on('turn.complete', ($, e, next) => {
    ctl?.onTurnComplete(e as { turnId?: string; agentId?: string })
    return next(e)
  })

  on('session.start', async ($, e, next) => {
    const stateDir = resolveStateDir({
      override: await $.env.get('PG_CLAUDE_CONTROL_STATE_DIR'),
      xdg: await $.env.get('XDG_STATE_HOME'),
      home: await $.env.get('HOME'),
    })
    const c = new Controller(stateDir, parseAllow(await $.env.get('PG_CLAUDE_CONTROL_RUN_ALLOW')))
    ctl = c
    const io: Io = {
      now: () => $.clock.now(),
      readText: async p => {
        const r = await $.fs.read(p)
        return typeof r === 'string' ? r : ''
      },
      writeText: (p, t) => $.fs.write(p, t),
      run: async (argv, init) => ({ exitCode: (await $.process.run(argv, init)).exitCode }),
      submit: async text => {
        await $.prompt.submit({ text })
      },
      abort: turnId => $.turn.abort({ turnId }),
      command: (command, args) => $.command.run({ command, args }),
      readPrompt: () => $.prompt.read(),
    }
    const started = await next(e)
    void (async () => {
      await io.run(['mkdir', '-p', `${stateDir}/ends`])
      for (;;) {
        const t0 = await io.now()
        let code: number | null = null
        try {
          const child = $.process.spawn({
            argv: ['bash', `${$.plugin.root}/scripts/listen.sh`],
            env: { PG_CLAUDE_CONTROL_STATE_DIR: stateDir },
          })
          let err = ''
          for await (const piece of child) {
            if ('stream' in piece) {
              if (piece.stream === 'stdout') {
                for (const w of c.ingest(io, piece.text)) void w
              } else {
                err += piece.text
                const nl = err.lastIndexOf('\n')
                if (nl >= 0) {
                  for (const l of err.slice(0, nl).split('\n')) {
                    const m = /^(\w+)(?::\s*(.*))?$/.exec(l.trim())
                    if (m) {
                      if (m[1] === 'inbox_dir_refused') refused = true
                      await c.event(io, m[1], { detail: m[2] ?? '' })
                    }
                  }
                  err = err.slice(nl + 1)
                }
              }
            } else {
              code = piece.code
            }
          }
        } catch (err2) {
          await c.event(io, 'listener_error', { error: String(err2) })
        }
        if (refused || code === 20) return // inbox directory refused: nothing to respawn into
        const d = c.respawn.onExit((await io.now()) - t0)
        if (d.giveUp) {
          await c.event(io, 'listener.gave_up', { tries: d.consecutive })
          return
        }
        await $.clock.sleep(d.pauseMs)
      }
    })()
    return started
  })

  on('session.end', async ($, e, next) => {
    const c = ctl
    if (c) {
      const reason = END_REASONS.includes(e.reason) ? e.reason : 'other'
      try {
        await c.writeEnd(
          {
            now: () => $.clock.now(),
            writeText: (p, t) => $.fs.write(p, t),
            run: async (argv, init) => ({ exitCode: (await $.process.run(argv, init)).exitCode }),
          },
          e.sessionId,
          reason,
        )
      } catch {
        // the end file is best effort; never delay the exit
      }
    }
    return next(e)
  })
}

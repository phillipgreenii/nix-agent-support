// Shared test helpers of the source-layer tests (not a test file itself, so the check does not run
// it). Choices: the fake clock is a sorted timer queue driven by advance(ms), which fires due
// timers in order and lets promise continuations settle (a macrotask) after each one; the stub host
// has the facade's shape and records every call into `calls` as "path" strings, with a small
// in-memory file system for fs.read (a missing path rejects, as the host does).
export interface FakeClock {
  now: () => number
  setTimer: (fn: () => void, delayMs: number) => { cancel(): void }
  advance: (ms: number) => Promise<void>
  pending: () => number
}

export const settle = (): Promise<void> => new Promise(r => setImmediate(r))

export function fakeClock(start = 1_000_000): FakeClock {
  let t = start
  let timers: Array<{ at: number; fn: () => void; live: boolean }> = []
  return {
    now: () => t,
    setTimer: (fn, delayMs) => {
      const tm = { at: t + delayMs, fn, live: true }
      timers.push(tm)
      return {
        cancel: () => {
          tm.live = false
        },
      }
    },
    advance: async ms => {
      const end = t + ms
      await settle()
      for (;;) {
        timers = timers.filter(x => x.live)
        const next = timers.filter(x => x.at <= end).sort((a, b) => a.at - b.at)[0]
        if (!next) break
        t = Math.max(t, next.at)
        next.live = false
        next.fn()
        await settle()
      }
      t = end
      await settle()
    },
    pending: () => timers.filter(x => x.live).length,
  }
}

export interface StubHost {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  host: any
  calls: string[]
  files: Map<string, string>
  state: { cwd: string; model: string }
}

export function stubHost(files: Record<string, string> = {}): StubHost {
  const calls: string[] = []
  const fileMap = new Map(Object.entries(files))
  const state = { cwd: '/work/repo', model: 'sonnet' }
  const host = {
    process: {
      run: async (...a: unknown[]) => {
        calls.push('process.run')
        void a
        return { exitCode: 0 }
      },
    },
    fs: {
      read: async (p: string) => {
        calls.push(`fs.read ${p}`)
        const v = fileMap.get(p)
        if (v === undefined) throw new Error(`ENOENT ${p}`)
        return v
      },
      write: async () => {
        calls.push('fs.write')
      },
    },
    http: {
      fetch: async () => {
        calls.push('http.fetch')
      },
    },
    config: {
      set: async () => {
        calls.push('config.set')
      },
    },
    ui: {
      invalidate: () => {
        calls.push('ui.invalidate')
      },
    },
    session: {
      model: async () => {
        calls.push('session.model')
        return state.model
      },
      cwd: async () => {
        calls.push('session.cwd')
        return state.cwd
      },
    },
  }
  return { host, calls, files: fileMap, state }
}

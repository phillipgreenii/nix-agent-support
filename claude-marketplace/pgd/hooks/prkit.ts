// Shared helpers of the PR source tests (not a test file itself, so the checks do not run it).
import { registerComponents } from './components'
import { startGitHead } from './gitHead'
import { resetSessionFacts } from './info-modal'
import { setOptions } from './options'
import { initSources, noteEvent, setVisible } from './source'
import { fakeClock, stubHost, type StubHost } from './testkit'
import { DEFAULT_OPTIONS, type PgdOptions } from './types'

export const HEAD = (b: string): string => `ref: refs/heads/${b}\n`
export const withPr = (prStatus: boolean): PgdOptions => ({
  ...DEFAULT_OPTIONS,
  // beads off: the dolt probe is another process.run user
  components: { ...DEFAULT_OPTIONS.components, beads: false, prStatus },
})

export type Run = (argv: string[], init: unknown) => Promise<unknown>
export const ok =
  (number = 12, reviewDecision = 'APPROVED'): Run =>
  async () => ({ exitCode: 0, stdout: JSON.stringify({ number, reviewDecision }) })

// One session: sources started, git head and PR component registered, the Info modal "shown" so
// the modal-fed PR source is active. `s` may be reused to start a second session on the same store.
export async function boot(opts: { branch?: string; run?: Run; options?: PgdOptions; s?: StubHost; repo?: StubHost['facts']['repo'] }) {
  const clock = fakeClock()
  const s = opts.s ?? stubHost()
  if (opts.branch !== undefined) s.files.set('/work/repo/.git/HEAD', HEAD(opts.branch))
  if (opts.repo !== undefined) s.facts.repo = opts.repo
  const commands: string[][] = []
  const run = opts.run ?? ok()
  s.host.process.run = async (argv: string[], init: unknown) => {
    s.calls.push('process.run')
    commands.push(argv)
    return run(argv, init)
  }
  const options = opts.options ?? withPr(true)
  resetSessionFacts()
  setOptions(options)
  initSources(s.host, { now: clock.now, setTimer: clock.setTimer })
  startGitHead(s.host)
  registerComponents(options, s.host)
  setVisible('modal', true)
  await clock.advance(1)
  return { clock, s, commands }
}


// Advance the clock in one-minute steps, reporting an engine event at each, so the idle rule (every
// interval times four after five quiet minutes) does not apply: a session in use is not idle.
export async function tick(clock: { advance(ms: number): Promise<void> }, ms: number): Promise<void> {
  for (let left = ms; left > 0; left -= 60_000) {
    noteEvent()
    await clock.advance(Math.min(60_000, left))
  }
}

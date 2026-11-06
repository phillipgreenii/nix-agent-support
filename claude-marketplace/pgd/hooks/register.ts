import { atom, read, update } from 'claude-code'
import type { Register } from 'claude-code'
import { registerComponents } from './components'
import { parseOptions, type ModApi } from './types'
import { setOptions } from './options'
import { initSources, requestInvalidate } from './source'
import { initContributions } from './contributions'
import { registerGitHeadHooks, startGitHead } from './gitHead'
import { registerModelEffortHooks, startModelEffort } from './modelEffort'
import { registerUsageHooks, startUsage } from './usage'
import { registerBandHooks } from './band'
import { registerAlertHooks } from './alerts'
import { initLog } from './log'
import { exportMetrics, initExporter } from './exporter'
import { hydrateToggles, setToggleStore } from './toggles'
import { registerModalEventHooks } from './modals'
import { registerLinkHooks } from './links'

// The display mod. Choices (the design leaves them to the builder, so they follow the
// pg-claude-control precedent):
//  - Delivery: the Home Manager module renders the options as one JSON document in the
//    environment variable PG_CLAUDE_DISPLAY_OPTIONS (home.sessionVariables), read here with
//    $.env.get at session.start, exactly as the control mod reads its settings. Absent or
//    malformed, the defaults apply (every component on).
//  - ModApi: declared locally in types.ts (tests run with stripped types and no engine); only the
//    Register type is imported from the engine, type-only.
//  - Serialization: JSON, parsed by parseOptions; the options check inspects the JSON the module
//    renders.
//  - Manifest: name (pgd), description and version follow the control mod's plugin.json shape.
//  - Registration shape (final, validated by `claude plugin validate`): the engine refuses a
//    module that passes `$` itself as an argument, so registerComponents(options, host) takes a
//    HOST FACADE instead of `$`. The facade is an object of closures built in the hook body below,
//    every member spelled as a `$.noun.method(...)` call site. A component is
//    (options, host) => void and touches host only when its flag is true; a test passes a stub
//    host (the same shape, recording every call). A component packet that needs another `$`
//    member adds one closure to the facade here; it never passes `$`. The engine also requires
//    `$.env.get`/`$.env.set` to take a LITERAL variable name, so the facade has no generic env
//    member: a component that needs a variable adds a purpose-named closure that spells the
//    literal name (as the options read below does).
//  - Source layer: the sources (source.ts) start inside session.start, only when enable is true,
//    after setOptions and initSources and before registerComponents. The event hooks that feed
//    them are registered at registration time by the register*Hooks(on) functions of the source
//    files (each takes `$` from its own callback, never as an argument) and read getOptions().
//    The facade also holds ui.invalidate, session.model, session.cwd and clock.after (the
//    source layer's setTimer is built from it).
const EXPORT_EVERY_MS = 60_000

// The toggle state (the band and each modal) is an atom, so it survives a hot reload: the module's
// own variables start over on a reload, the atom stays. The shape tag declines a value an earlier
// version stored in another form.
const togglesAtom = atom({ plugin: 'pgd', key: 'toggles' } as const, {}, { shape: 'toggles-1' })

// Export the metrics every 60 seconds, never awaited; a no-op without a configured endpoint.
function startExportTimer(host: ModApi): void {
  const tick = (): void => {
    void exportMetrics()
    host.clock.after(EXPORT_EVERY_MS, tick)
  }
  host.clock.after(EXPORT_EVERY_MS, tick)
}

export const register: Register = on => {
  registerUsageHooks(on)
  registerModelEffortHooks(on)
  registerGitHeadHooks(on)
  registerBandHooks(on)
  registerAlertHooks(on)
  registerModalEventHooks(on)
  registerLinkHooks(on)
  on('session.start', async ($, e, next) => {
    const options = parseOptions(await $.env.get('PG_CLAUDE_DISPLAY_OPTIONS'))
    const host = {
      process: { run: (argv, init) => $.process.run(argv, init) },
      fs: { read: p => $.fs.read(p), write: (p, t) => $.fs.write(p, t) },
      http: { fetch: (url, init) => $.http.fetch(url, init) },
      config: { set: (a: unknown) => $.config.set(a) },
      prompt: { fill: (a: unknown) => $.prompt.fill(a) },
      store: { get: k => $.store.get(k), set: (k, v) => $.store.set(k, v) },
      ui: {
        invalidate: () => $.ui.invalidate('ui.render'),
        open: (a: unknown) => $.ui.open(a),
        close: (a: unknown) => $.ui.close(a),
      },
      command: { register: (spec: unknown) => $.command.register(spec) },
      session: {
        model: () => $.session.model(),
        cwd: () => $.session.cwd(),
        id: () => $.session.id(),
        version: () => $.session.version(),
        repo: () => $.session.repo(),
      },
      env: {
        stateHome: () => $.env.get('XDG_STATE_HOME'),
        home: () => $.env.get('HOME'),
      },
      engine: { create: (methods: unknown) => $.engine.create(methods) },
      clock: { after: (ms: number, fn: () => void) => $.clock.after(ms, fn) },
    }
    setOptions(options)
    // Hydrate the toggles from the atom before anything draws, and persist every later change.
    try {
      hydrateToggles(await read($, togglesAtom))
    } catch {
      // a state read that fails leaves the defaults: the band shown, every modal closed
    }
    setToggleStore({ save: state => void Promise.resolve(update($, togglesAtom, () => state)).catch(() => undefined) })
    if (options.enable) {
      initLog(host)
      initExporter(host)
      initSources(host, { setTimer: (fn, ms) => host.clock.after(ms, fn) })
      initContributions({ now: () => Date.now(), setTimer: (fn, ms) => host.clock.after(ms, fn), requestInvalidate })
      startExportTimer(host)
      startUsage(host)
      startModelEffort(host)
      startGitHead(host)
    }
    registerComponents(options, host)
    return next(e)
  })
}

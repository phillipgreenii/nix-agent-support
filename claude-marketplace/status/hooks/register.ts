import type { Register } from 'claude-code'
import { registerComponents } from './components'
import { parseOptions } from './types'

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
//  - Manifest: name (status), description and version follow the control mod's plugin.json shape.
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
export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const options = parseOptions(await $.env.get('PG_CLAUDE_DISPLAY_OPTIONS'))
    registerComponents(options, {
      process: { run: (argv, init) => $.process.run(argv, init) },
      fs: { read: p => $.fs.read(p), write: (p, t) => $.fs.write(p, t) },
      http: { fetch: (url, init) => $.http.fetch(url, init) },
      config: { set: (k, v) => $.config.set(k, v) },
    })
    return next(e)
  })
}

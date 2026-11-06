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
//  - Manifest: name, description and version follow the control mod's plugin.json shape.
export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    const options = parseOptions(await $.env.get('PG_CLAUDE_DISPLAY_OPTIONS'))
    registerComponents(options, $)
    return next(e)
  })
}

import type { PgdOptions, ModApi } from './types'
import { registerModalBuilder, setPaneApi, registerModalHooks, applyBandState } from './modal'
import { infoSpec, loadSessionFacts } from './info-modal'
import { startCompaction } from './compaction'
import { contextSpec, startLastCompaction } from './context-modal'
import { registerFocusToggleCommand, registerToggleCommands, registerToggleCommandSpecs } from './toggleCommands'

// The modal component: governed by the top-level enable only (the opt-in table lists no flag for
// the Info and Context modals). Touches the host facade only through closures; at registration it
// calls command.register for each toggle command and nothing else (session facts load when the
// Info modal is first opened).
export function registerModals(_options: PgdOptions, host: ModApi): void {
  setPaneApi({
    open: a => host.ui.open(a),
    close: a => host.ui.close(a),
    invalidate: () => host.ui.invalidate(),
  })
  startLastCompaction()
  startCompaction(host)
  registerModalBuilder('info', infoSpec, undefined, () => loadSessionFacts(host))
  registerModalBuilder('context', contextSpec)
  applyBandState()
  registerToggleCommandSpecs(host)
}

// Registration-time hooks (they take $ from their own callback).
export function registerModalEventHooks(on: ModApi): void {
  registerModalHooks(on)
  registerToggleCommands(on)
  registerFocusToggleCommand(on)
}

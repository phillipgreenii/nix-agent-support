// Types contract of the pgd noun. The noun carries data only: every method takes one object and
// returns a Promise, and there is no callback argument and no action field (a contributed button
// is out of scope). A contributor pushes when its state changes; pgd never calls back into it.

// Where a contribution is shown: `alert` joins the one-row alert line (plain text), `focus` is an
// item in the Focus pane.
export type PgdSurface = 'alert' | 'focus'

export type PgdPutArgs = {
  // Identifies the contribution; putting the same id again replaces it and restarts its ttlMs.
  id: string
  surface: PgdSurface
  // Larger is higher: the alert row lists higher priorities first and drops the lowest first.
  priority: number
  text: string
  // Stored with the contribution; the alert row is plain text and does not use it.
  color?: string
  // The contribution disappears this many milliseconds after the last put; absent, it never expires.
  ttlMs?: number
  // Focus surface only; an alert contribution with a url is rejected.
  url?: string
}

export type PgdRemoveArgs = { id: string }

export interface PgdNoun {
  // Adds or replaces a contribution. Rejects with an Error naming the bad field.
  put(args: PgdPutArgs): Promise<void>
  // Removes a contribution; an unknown id is not an error.
  remove(args: PgdRemoveArgs): Promise<void>
}

// The mod's $.state contract: the toggle state (the band and each modal) kept in an atom so it
// survives a hot reload. Each value is declared here under the mod's name. The pgd noun is
// declared on the engine's interface, added by the plugin's engine.create hook.
declare module 'claude-code' {
  interface PluginState {
    pgd: { toggles: Shaped<Record<string, boolean>> }
  }
  interface EngineInterface {
    pgd: PgdNoun
  }
}

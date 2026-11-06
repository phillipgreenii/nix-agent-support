// The mod's $.state contract: the toggle state (the band and each modal) kept in an atom so it
// survives a hot reload. Each value is declared here under the mod's name.
declare module 'claude-code' {
  interface PluginState {
    pgd: { toggles: Shaped<Record<string, boolean>> }
  }
}

// Shared types contract of the display mod. Type-only (plus the defaults and parser below), so a
// test run with stripped types loads it without the engine.

// The host API handed to every hook as `$`. Declared locally rather than imported from the
// engine's declarations: the node tests run with stripped types and no engine, and a component
// only ever touches `$` through members the engine documents.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type ModApi = any

export interface DisplayComponents {
  beads: boolean
  assume: boolean
  sessionMode: boolean
  prStatus: boolean
  recorderName: boolean
  links: boolean
  modelEffort: boolean
  focus: boolean
}

export interface DisplayLink {
  name: string
  pattern: string
  url: string
}

export interface AlertThresholds {
  contextWarn: number
  contextAlert: number
  limit: number
}

export interface DisplayOptions {
  enable: boolean
  components: DisplayComponents
  links: DisplayLink[]
  alerts: { thresholds: AlertThresholds }
}

// Defaults match today's shell line: every component on, no links, thresholds 60 / 80 / 80.
export const DEFAULT_OPTIONS: DisplayOptions = {
  enable: true,
  components: {
    beads: true,
    assume: true,
    sessionMode: true,
    prStatus: true,
    recorderName: true,
    links: true,
    modelEffort: true,
    focus: true,
  },
  links: [],
  alerts: { thresholds: { contextWarn: 60, contextAlert: 80, limit: 80 } },
}

// Parse the rendered options (JSON). Missing or malformed input yields the defaults; a present
// field overrides its default only when it has the right type.
export function parseOptions(raw: string | undefined | null): DisplayOptions {
  const d = DEFAULT_OPTIONS
  let o: Record<string, unknown> = {}
  try {
    const v: unknown = raw ? JSON.parse(raw) : {}
    if (v && typeof v === 'object' && !Array.isArray(v)) o = v as Record<string, unknown>
  } catch {
    o = {}
  }
  const obj = (v: unknown): Record<string, unknown> =>
    v && typeof v === 'object' && !Array.isArray(v) ? (v as Record<string, unknown>) : {}
  const comps = obj(o.components)
  const components = { ...d.components }
  for (const k of Object.keys(components) as Array<keyof DisplayComponents>) {
    if (typeof comps[k] === 'boolean') components[k] = comps[k] as boolean
  }
  const th = obj(obj(o.alerts).thresholds)
  const thresholds = { ...d.alerts.thresholds }
  for (const k of Object.keys(thresholds) as Array<keyof AlertThresholds>) {
    if (typeof th[k] === 'number') thresholds[k] = th[k] as number
  }
  const links = Array.isArray(o.links)
    ? (o.links as unknown[]).filter(
        (l): l is DisplayLink =>
          !!l &&
          typeof (l as DisplayLink).name === 'string' &&
          typeof (l as DisplayLink).pattern === 'string' &&
          typeof (l as DisplayLink).url === 'string',
      )
    : []
  return {
    enable: typeof o.enable === 'boolean' ? o.enable : d.enable,
    components,
    links,
    alerts: { thresholds },
  }
}

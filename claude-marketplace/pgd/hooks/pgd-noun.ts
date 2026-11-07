// The pgd noun: data in, Promise out, one object per call. It has no callback argument and no
// action field. put and remove write the contribution store; validation names the bad field.
import { putContribution, removeContribution } from './contributions'
import { getOptions } from './options'
import type { PgdOptions } from './types'
import type { PutArgs, RemoveArgs } from '../types/pgd'

const bad = (field: string, why: string): Error => new Error(`pgd.put: invalid ${field}: ${why}`)

export function validatePut(a: unknown): PutArgs {
  const o = (a ?? {}) as Record<string, unknown>
  if (typeof o.id !== 'string' || o.id === '') throw bad('id', 'a non-empty string is required')
  if (o.surface !== 'alert' && o.surface !== 'focus') throw bad('surface', 'must be alert or focus')
  if (typeof o.priority !== 'number' || !Number.isFinite(o.priority)) throw bad('priority', 'a number is required')
  if (typeof o.text !== 'string') throw bad('text', 'a string is required')
  if (o.color !== undefined && typeof o.color !== 'string') throw bad('color', 'must be a string')
  if (o.ttlMs !== undefined && (typeof o.ttlMs !== 'number' || !Number.isFinite(o.ttlMs) || o.ttlMs <= 0)) {
    throw bad('ttlMs', 'must be a positive number')
  }
  if (o.url !== undefined) {
    if (o.surface === 'alert') throw bad('url', 'only a focus contribution may carry a url')
    if (typeof o.url !== 'string') throw bad('url', 'must be a string')
  }
  const out: PutArgs = { id: o.id, surface: o.surface, priority: o.priority, text: o.text }
  if (o.color !== undefined) out.color = o.color as string
  if (o.ttlMs !== undefined) out.ttlMs = o.ttlMs as number
  if (o.url !== undefined) out.url = o.url as string
  return out
}

// The noun's methods. `options` is read at each call (the noun is built in engine.create, before
// session.start parses the options): until the options are set, or with enable false, a call
// resolves undefined and stores nothing. With components.focus off a focus contribution likewise
// resolves undefined and stores nothing.
export function pgdMethods(options: PgdOptions | (() => PgdOptions | undefined)): {
  put: (a: PutArgs) => Promise<void>
  remove: (a: RemoveArgs) => Promise<void>
} {
  const current = (): PgdOptions | undefined => (typeof options === 'function' ? options() : options)
  return {
    put: async a => {
      const args = validatePut(a)
      const o = current()
      if (!o || !o.enable) return
      if (args.surface === 'focus' && !o.components.focus) return
      putContribution(args)
    },
    remove: async a => {
      if (!current()?.enable) return
      const id = (a as { id?: unknown } | undefined)?.id
      if (typeof id === 'string') removeContribution(id)
    },
  }
}

// Adds the pgd noun to `$` in engine.create (post-order: the steps beneath build `$`, this plugin
// adds its noun). A plugin cannot add a noun any other way; there is no $.engine.create.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function registerPgdNoun(on: any): void {
  on('engine.create', async (_$: unknown, e: unknown, next: (e: unknown) => Promise<object>) => {
    const built = await next(e)
    return { ...built, pgd: pgdMethods(getOptions) }
  })
}

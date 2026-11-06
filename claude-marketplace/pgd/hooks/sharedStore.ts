// Shared entries across sessions, kept in the plugin's `$.store`: one key per source
// (shared:<id>, or shared:<id>:<key> for a keyed source), each value { value, fetchedAt }, never
// one map under a single key (a get, merge and set of one map across sessions loses other
// sessions' entries). Last writer wins per key, which a cache tolerates. There is no file
// fallback: a store call that throws makes that one call unshared (the session fetches for itself)
// and the store is tried again on the next call. Values stay small, because every set rewrites the
// whole store, which is capped at 4 MiB.
export interface StoreLike {
  get(key: string): Promise<unknown> | unknown
  set(key: string, value: unknown): Promise<unknown> | unknown
}

export interface SharedEntry {
  value: unknown
  fetchedAt: number
}

export interface SharedStore {
  read(entryKey: string): Promise<SharedEntry | undefined>
  write(entryKey: string, value: unknown): Promise<void>
}

export function entryKey(id: string, key?: string): string {
  return key === undefined ? `shared:${id}` : `shared:${id}:${key}`
}

function isEntry(v: unknown): v is SharedEntry {
  return (
    !!v &&
    typeof v === 'object' &&
    typeof (v as SharedEntry).fetchedAt === 'number' &&
    'value' in (v as object)
  )
}

export function createSharedStore({
  store,
  now,
}: {
  store: StoreLike | undefined
  now: () => number
}): SharedStore {
  return {
    // The entry under the key, or undefined when absent, malformed or the store throws.
    async read(key) {
      try {
        if (!store) return undefined
        const v = await store.get(key)
        return isEntry(v) ? v : undefined
      } catch {
        return undefined
      }
    },
    async write(key, value) {
      try {
        if (!store) return
        await store.set(key, { value, fetchedAt: now() })
      } catch {
        // unshared for this call; the next call tries the store again
      }
    },
  }
}

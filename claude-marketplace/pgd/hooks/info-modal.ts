import { readCached } from './source'
import type { ModalSpec, ModalItem } from './modal'
import type { GitHead } from './gitHead'
import type { ModApi } from './types'

// The Info modal: one item per line. Rows: name (only when a name source exists: the session-name
// component is a later packet, so today the row is omitted rather than repeating the session id),
// session id, actor (the session id cut to 8 characters and an ellipsis, as the actor is the
// session that claims work), version, repo, worktree, branch and, when the PR source has a value,
// the PR with its review state coloured. A row whose value is missing is omitted: with the .git
// reads failing there are no branch and worktree rows, and with no PR value there is no PR row.
export type { PrValue } from './pr'
import type { PrValue } from './pr'

interface SessionFacts {
  id?: string
  version?: string
  repo?: string
}

let facts: SessionFacts = {}
let pending: Promise<void> | undefined

export function resetSessionFacts(): void {
  facts = {}
  pending = undefined
}

// Read the session accessors once, through the facade, before the modal first opens (never from a
// draw); later calls reuse the first answer. A failed read leaves those rows omitted.
export function loadSessionFacts(host: ModApi): Promise<void> {
  pending ??= (async () => {
    try {
      const next: SessionFacts = {}
      next.id = String((await host.session.id()) ?? '') || undefined
      next.version = (await host.session.version?.())?.version
      const repo = await host.session.repo?.()
      if (repo) next.repo = repo.name ?? repo.remote ?? repo.root
      facts = next
    } catch {
      // keep what is known; the rows stay omitted
    }
  })()
  return pending
}

export function actorOf(id: string): string {
  return id.length > 8 ? `${id.slice(0, 8)}…` : id
}

const REVIEW_COLORS: Record<string, string> = {
  APPROVED: 'green',
  CHANGES_REQUESTED: 'red',
  REVIEW_REQUIRED: 'yellow',
}

export function reviewColor(state: string): string | undefined {
  return REVIEW_COLORS[state.toUpperCase().replace(/[ -]/g, '_')]
}

export function infoSpec(): ModalSpec {
  const items: ModalItem[] = []
  const name = readCached<string>('sessionName')
  if (name) items.push({ label: 'name', value: name })
  if (facts.id) {
    items.push({ label: 'session id', value: facts.id })
    items.push({ label: 'actor', value: actorOf(facts.id) })
  }
  if (facts.version) items.push({ label: 'version', value: facts.version })
  if (facts.repo) items.push({ label: 'repo', value: facts.repo })
  const git = readCached<GitHead>('gitHead')
  if (git?.worktree) items.push({ label: 'worktree', value: git.worktree })
  if (git?.branch) items.push({ label: 'branch', value: git.branch })
  else if (git?.detached) items.push({ label: 'branch', value: `detached at ${git.detached.slice(0, 8)}` })
  const pr = readCached<PrValue>('pr')
  if (pr) {
    const color = reviewColor(pr.reviewState)
    const state = pr.reviewState.toLowerCase().replace(/_/g, ' ')
    items.push({ label: 'PR', value: state ? `#${pr.number} (${state})` : `#${pr.number}`, ...(color ? { color } : {}) })
  }
  return { id: 'info', title: 'Info', items, actions: [] }
}

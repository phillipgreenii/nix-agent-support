import { registerBandButton, type Level } from './band'
import { defineSource, setValue } from './source'
import type { ModApi, PgdOptions } from './types'

// The Assume button's display slice: a band button showing the AWS profile the session's own
// processes run with, coloured by the profile name. The source is event-driven (intervalMs 0,
// feeds the band) and is seeded once at session start from the Claude process's AWS_PROFILE, read
// through the facade's env.awsProfile(); a later packet adds the expiry and the setValue calls that
// keep this value shape. There is no modal, Apply, Clear or alert here, and no profile alert
// anywhere: the button is the only place the profile is shown.
export const ASSUME_ID = 'assume'
export const ASSUME_ORDER = 50
export const ASSUME_LABEL = 'Assume'

export interface AssumeValue {
  role?: string
  expiresAt?: number
}

// The colour rule, matching the profile name ignoring case: unset or empty is dim, a name
// containing prod is red (checked first, so a name with both is red), otherwise a name containing
// dev is green, any other value is yellow.
export function assumeLevel(profile: string | undefined): Level {
  if (!profile) return 'dim'
  const p = profile.toLowerCase()
  if (p.includes('prod')) return 'red'
  if (p.includes('dev')) return 'green'
  return 'yellow'
}

export function assumeLabel(profile: string | undefined): string {
  return profile ? profile : ASSUME_LABEL
}

async function seed(host: ModApi): Promise<void> {
  let role: string | undefined
  try {
    const v = await host.env.awsProfile()
    role = typeof v === 'string' && v !== '' ? v : undefined
  } catch {
    role = undefined
  }
  setValue(ASSUME_ID, { role } satisfies AssumeValue)
}

export function startAssume(_options: PgdOptions, host: ModApi): void {
  defineSource<AssumeValue>({ id: ASSUME_ID, intervalMs: 0, feeds: ['band'] })
  registerBandButton({
    id: ASSUME_ID,
    order: ASSUME_ORDER,
    label: v => assumeLabel(v.assumeRole),
    level: v => assumeLevel(v.assumeRole),
  })
  void seed(host)
}

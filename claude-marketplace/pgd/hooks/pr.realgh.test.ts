// The PR source against the REAL gh binary in a hermetic environment: a throwaway HOME and gh
// config directory, no token variable and no network, so gh itself fails (no authentication) and
// that real failure path is observed. A mock of gh does not count, so nothing here stubs it.
//
// This file imports node modules, which the engine's own test loader refuses (the same as
// sharedStore.multiprocess.test.ts, which `claude plugin test` reports as "did not load"); the nix
// check test-pgd-mod runs it under node with pkgs.gh on its inputs. Unlike the multi-process suite
// it is fast, single-process and hermetic, so it stays in the unit gate.
import { execFile } from 'node:child_process'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { expect, test } from 'claude-code/testing'
import { infoSpec } from './info-modal'
import { boot, type Run } from './prkit'
import { readCached } from './source'
import { settle } from './testkit'

function hermeticEnv(path: string): Record<string, string> {
  const dir = mkdtempSync(`${tmpdir()}/pgd-gh-`)
  return {
    PATH: path,
    HOME: dir,
    GH_CONFIG_DIR: `${dir}/gh`,
    XDG_CONFIG_HOME: `${dir}/xdg`,
    GH_PROMPT_DISABLED: '1',
    GH_NO_UPDATE_NOTIFIER: '1',
  }
}

// A process.run that runs the real binary and rejects on a non-zero exit; `last` is the newest run.
function realRun(env: Record<string, string>): { run: Run; last: () => Promise<unknown> } {
  let last: Promise<unknown> = Promise.resolve()
  const run: Run = argv => {
    last = new Promise((resolve, reject) => {
      execFile(argv[0], argv.slice(1), { env, timeout: 20_000 }, (err, stdout, stderr) => {
        if (err) reject(Object.assign(err, { stderr }))
        else resolve({ exitCode: 0, stdout })
      })
    })
    return last
  }
  return { run, last: () => last }
}

async function failedFetch(path: string) {
  const { run, last } = realRun(hermeticEnv(path))
  const { commands } = await boot({ branch: 'feature/x', run })
  const before = JSON.stringify({ git: readCached('gitHead'), model: readCached('model') })
  await last().catch(() => undefined)
  await settle()
  return { commands, before }
}

test('the real gh, with no token and no network, fails the PR fetch: the row is omitted and nothing else changes', async () => {
  // gh is present and runs: its own version output proves the failure below is gh's, not ENOENT
  const probe = realRun(hermeticEnv(process.env.PATH ?? ''))
  await probe.run(['gh', '--version'], {})
  const { commands, before } = await failedFetch(process.env.PATH ?? '')
  expect(commands).toHaveLength(1)
  expect(commands[0].slice(0, 3)).toEqual(['gh', 'pr', 'view'])
  expect(commands[0][commands[0].indexOf('-R') + 1]).toBe('owner/repo')
  expect(readCached('pr')).toBe(undefined)
  expect(infoSpec().items.some(i => i.label === 'PR')).toBe(false)
  expect(JSON.stringify({ git: readCached('gitHead'), model: readCached('model') })).toBe(before)
})

test('with gh absent from PATH the fetch fails like any other and nothing else changes', async () => {
  const { commands, before } = await failedFetch('/nonexistent-pgd-path')
  expect(commands).toHaveLength(1)
  expect(readCached('pr')).toBe(undefined)
  expect(infoSpec().items.some(i => i.label === 'PR')).toBe(false)
  expect(JSON.stringify({ git: readCached('gitHead'), model: readCached('model') })).toBe(before)
})

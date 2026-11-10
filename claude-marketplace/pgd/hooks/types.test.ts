// Tests of the options parser for the link rule kinds and the top-level github object.
import { expect, test } from 'claude-code/testing'
import { DEFAULT_OPTIONS, parseOptions, parseOptionsDetailed } from './types'
import { register } from './register'
import { flushLog } from './log'

const parse = (o: unknown) => parseOptionsDetailed(JSON.stringify(o))

test('the defaults carry a github object with the public host and no repos', () => {
  expect(DEFAULT_OPTIONS.github).toEqual({ host: 'github.com', repos: [] })
  expect(parseOptions(undefined).github).toEqual({ host: 'github.com', repos: [] })
})

test('an entry without kind is a regex rule, unchanged', () => {
  const r = parse({ links: [{ name: 'ex', pattern: 'EX-[0-9]+', url: 'https://x.invalid/{0}' }] })
  expect(r.options.links).toEqual([{ name: 'ex', pattern: 'EX-[0-9]+', url: 'https://x.invalid/{0}' }])
  expect(r.skipped).toEqual([])
})

test('an explicit regex kind is kept; a regex rule without pattern or url is skipped with one message', () => {
  const r = parse({
    links: [
      { kind: 'regex', name: 'a', pattern: 'A', url: 'https://x.invalid/{0}' },
      { kind: 'regex', name: 'b', pattern: 'B' },
      { name: 'c', url: 'https://x.invalid/{0}' },
    ],
  })
  expect(r.options.links.map(l => l.name)).toEqual(['a'])
  expect(r.skipped).toHaveLength(2)
  expect(r.skipped[0].includes('"b"')).toBe(true)
  expect(r.skipped[1].includes('"c"')).toBe(true)
})

test('null reads as absent, and unknown fields are ignored', () => {
  const r = parse({
    extra: 1,
    enable: null,
    github: null,
    links: [
      { kind: null, name: 'n', pattern: 'N', url: 'https://x.invalid/{0}', options: null, future: true },
      { kind: 'github-pr', name: 'pr', options: null },
    ],
  })
  expect(r.options.enable).toBe(true)
  expect(r.options.github).toEqual({ host: 'github.com', repos: [] })
  expect(r.options.links).toEqual([
    { name: 'n', pattern: 'N', url: 'https://x.invalid/{0}' },
    { kind: 'github-pr', name: 'pr', options: { sessionRepoFallback: false } },
  ])
  expect(r.skipped).toEqual([])
})

test('github host and repos are read; a malformed repo is dropped with one message', () => {
  const r = parse({ github: { host: 'git.example.invalid', repos: ['own/repo', 'bad', 'a/b/c', 'x y/z', 7] } })
  expect(r.options.github).toEqual({ host: 'git.example.invalid', repos: ['own/repo'] })
  expect(r.skipped).toHaveLength(4)
})

test('github-pr reads sessionRepoFallback and rejects a non-boolean', () => {
  const ok = parse({ links: [{ kind: 'github-pr', name: 'pr', options: { sessionRepoFallback: true } }] })
  expect(ok.options.links).toEqual([{ kind: 'github-pr', name: 'pr', options: { sessionRepoFallback: true } }])
  const bad = parse({ links: [{ kind: 'github-pr', name: 'pr', options: { sessionRepoFallback: 'yes' } }] })
  expect(bad.options.links).toEqual([])
  expect(bad.skipped).toHaveLength(1)
})

test('jira: valid options pass; a bad baseUrl, bad key or missing keys skip the rule alone', () => {
  const good = { kind: 'jira', name: 'tk', options: { baseUrl: 'https://tracker.invalid/browse', keys: ['AB', 'C_D1'] } }
  const r = parse({
    links: [
      good,
      { kind: 'jira', name: 'ftp', options: { baseUrl: 'ftp://tracker.invalid', keys: ['AB'] } },
      { kind: 'jira', name: 'nourl', options: { keys: ['AB'] } },
      { kind: 'jira', name: 'junk', options: { baseUrl: 'not a url', keys: ['AB'] } },
      { kind: 'jira', name: 'lower', options: { baseUrl: 'https://tracker.invalid/browse', keys: ['ab'] } },
      { kind: 'jira', name: 'digit', options: { baseUrl: 'https://tracker.invalid/browse', keys: ['1A'] } },
      { kind: 'jira', name: 'empty', options: { baseUrl: 'https://tracker.invalid/browse', keys: [] } },
      { kind: 'jira', name: 'nokeys', options: { baseUrl: 'https://tracker.invalid/browse' } },
    ],
  })
  expect(r.options.links).toEqual([good])
  expect(r.skipped).toHaveLength(7)
  for (const n of ['ftp', 'nourl', 'junk', 'lower', 'digit', 'empty', 'nokeys']) {
    expect(r.skipped.filter(m => m.includes(`"${n}"`))).toHaveLength(1)
  }
})

test('file-ref: the default template is filled; scheme allowlist and {path} are enforced', () => {
  const r = parse({
    links: [
      { kind: 'file-ref', name: 'def' },
      { kind: 'file-ref', name: 'ed', options: { localTemplate: 'vscode://file{path}:{line}:{col}' } },
      { kind: 'file-ref', name: 'js', options: { localTemplate: 'javascript:{path}' } },
      { kind: 'file-ref', name: 'http', options: { localTemplate: 'https://h.invalid{path}' } },
      { kind: 'file-ref', name: 'nopath', options: { localTemplate: 'file://x:{line}' } },
      { kind: 'file-ref', name: 'noscheme', options: { localTemplate: '{path}' } },
    ],
  })
  expect(r.options.links).toEqual([
    { kind: 'file-ref', name: 'def', options: { localTemplate: 'file://{path}:{line}' } },
    { kind: 'file-ref', name: 'ed', options: { localTemplate: 'vscode://file{path}:{line}:{col}' } },
  ])
  expect(r.skipped).toHaveLength(4)
})

test('an unknown kind or a non-object entry is skipped alone with one message', () => {
  const r = parse({
    links: [
      { kind: 'wiki', name: 'w' },
      'text',
      null,
      { name: 'ok', pattern: 'O', url: 'https://x.invalid/{0}' },
    ],
  })
  expect(r.options.links.map(l => l.name)).toEqual(['ok'])
  expect(r.skipped).toHaveLength(3)
  expect(r.skipped[0].includes('"w"')).toBe(true)
  expect(r.skipped[0].includes('unknown kind')).toBe(true)
})

test('parseOptions keeps returning the options alone', () => {
  const o = parseOptions(JSON.stringify({ links: [{ kind: 'wiki', name: 'w' }] }))
  expect(o.links).toEqual([])
  expect(Object.keys(o).sort()).toEqual(['alerts', 'components', 'enable', 'github', 'links', 'metrics'])
})

// The skipped-rule warn must reach the log file: parseOptions runs before the log has a host, so
// register.ts logs the messages after initLog. Driven through the real session.start hook.
test('session.start writes one warn per skipped rule to the log file', async () => {
  const hooks: Array<{ name: string; fn: (...a: unknown[]) => unknown }> = []
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(register as any)((name: string, ...rest: unknown[]) => {
    hooks.push({ name, fn: rest[rest.length - 1] as (...a: unknown[]) => unknown })
  })
  const start = hooks.find(h => h.name === 'session.start')!
  const written: Array<{ path: string; text: string }> = []
  const options = JSON.stringify({
    links: [
      { kind: 'wiki', name: 'w' },
      { kind: 'jira', name: 'tk', options: { baseUrl: 'https://tracker.invalid/browse', keys: [] } },
    ],
  })
  const answer = (path: string, args: unknown[]): unknown => {
    if (path === '$.env.get') {
      if (args[0] === 'PG_CLAUDE_DISPLAY_OPTIONS') return Promise.resolve(options)
      if (args[0] === 'XDG_STATE_HOME') return Promise.resolve('/state')
      return Promise.resolve(undefined)
    }
    if (path === '$.session.id') return Promise.resolve('sess-links')
    if (path === '$.fs.write') {
      written.push({ path: String(args[0]), text: String(args[1]) })
      return Promise.resolve(undefined)
    }
    return Promise.resolve(undefined)
  }
  const make = (path: string): unknown =>
    new Proxy(() => undefined, {
      get: (_t, k) => make(`${path}.${String(k)}`),
      apply: (_t, _this, args) => answer(path, args),
    })
  await start.fn(make('$'), {}, async () => undefined)
  await flushLog()
  const lines = written
    .filter(w => w.path === '/state/pgd/sess-links.log')
    .map(w => w.text)
    .pop()!
    .trim()
    .split('\n')
    .map(l => JSON.parse(l) as { level: string; message: string })
  const warns = lines.filter(l => l.level === 'warn')
  expect(warns).toHaveLength(2)
  expect(warns[0].message.includes('"w"')).toBe(true)
  expect(warns[1].message.includes('"tk"')).toBe(true)
})

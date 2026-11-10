// Tests of the file-ref link kind, through the real rewrite with real strings.
import { expect, test } from 'claude-code/testing'
import { registerFileRefKind } from './fileRef'
import { registerJiraKind } from './jira'
import { resetLinkMemo, rewriteLinks, type LinkContext, type LinkRule } from './links'

registerFileRefKind()
registerJiraKind()

const rule = (localTemplate?: string): LinkRule => ({
  kind: 'file-ref',
  name: 'files',
  options: localTemplate === undefined ? undefined : { localTemplate },
}) as LinkRule
const ctx: LinkContext = { cwd: '/work/proj', home: '/home/u', knownRepos: ['acme/widgets'] }
const run = (text: string, context: LinkContext | undefined = ctx, template?: string): string => {
  resetLinkMemo()
  return rewriteLinks(text, [rule(template)], context)
}

test('a relative path joins onto cwd; an absolute path is used as is', () => {
  expect(run('see foo.go:42 now')).toBe('see [foo.go:42](file:///work/proj/foo.go:42) now')
  expect(run('see /abs/p.go:7')).toBe('see [/abs/p.go:7](file:///abs/p.go:7)')
  expect(run('see ../x.go:3')).toBe('see [../x.go:3](file:///work/x.go:3)')
  expect(run('see ./a/../b/x.go:3')).toBe('see [./a/../b/x.go:3](file:///work/proj/b/x.go:3)')
})

test('tilde expands from the snapshot home and declines without it', () => {
  expect(run('~/x.go:1')).toBe('[~/x.go:1](file:///home/u/x.go:1)')
  expect(run('~/x.go:1', { cwd: '/work/proj' })).toBe('~/x.go:1')
})

test('relative paths decline before the snapshot loads; absolute ones do not', () => {
  expect(run('foo.go:42', undefined)).toBe('foo.go:42')
  expect(run('/abs/p.go:7', undefined)).toBe('[/abs/p.go:7](file:///abs/p.go:7)')
})

test('punctuation and ranges', () => {
  expect(run('foo.go:42.')).toBe('[foo.go:42](file:///work/proj/foo.go:42).')
  expect(run('(foo.go:42)')).toBe('([foo.go:42](file:///work/proj/foo.go:42))')
  expect(run('foo.go:42, bar.go:1;')).toBe(
    '[foo.go:42](file:///work/proj/foo.go:42), [bar.go:1](file:///work/proj/bar.go:1);',
  )
  expect(run('foo.go:42-50')).toBe('[foo.go:42-50](file:///work/proj/foo.go:42)')
  expect(run('foo.go:42:7')).toBe('[foo.go:42:7](file:///work/proj/foo.go:42)')
})

test('link text and url stay intact for underscores and parentheses', () => {
  expect(run('__init__.py:3')).toBe('[\\_\\_init\\_\\_.py:3](file:///work/proj/__init__.py:3)')
  expect(run('app/(g)/page.tsx:3')).toBe('[app/(g)/page.tsx:3](file:///work/proj/app/%28g%29/page.tsx:3)')
})

test('the template gets {path} with its leading slash and {col} defaulting to 1', () => {
  expect(run('a.go:5', ctx, 'vscode://file{path}:{line}:{col}')).toBe(
    '[a.go:5](vscode://file/work/proj/a.go:5:1)',
  )
  expect(run('a.go:5:9', ctx, 'vscode://file{path}:{line}:{col}')).toBe(
    '[a.go:5:9](vscode://file/work/proj/a.go:5:9)',
  )
  expect(run('a.go:5', ctx, 'file://{path}#L{line}')).toBe('[a.go:5](file:///work/proj/a.go#L5)')
  expect(run('a.go:5', ctx, 'vscode://file{path}:{line}:{col}')).not.toContain('undefined')
})

test('segments are encoded, spaces and special characters included', () => {
  expect(run('/a/b+c@d(1).go:2')).toBe('[/a/b+c@d(1).go:2](file:///a/b%2Bc%40d%281%29.go:2)')
})

test('a launchable file type declines under the file scheme only', () => {
  for (const f of ['run.sh:1', 'x.app:1', 'a.command:2', 'b.pkg:3', 'c.dmg:4', 'd.workflow:5', 'e.terminal:6', 'f.tool:7']) {
    expect(run(f)).toBe(f)
  }
  expect(run('run.sh:1', ctx, 'vscode://file{path}:{line}')).toBe('[run.sh:1](vscode://file/work/proj/run.sh:1)')
})

test('remote form needs a known owner/repo before the first colon', () => {
  const known: LinkContext = { ...ctx, githubHost: 'git.example' }
  expect(run('acme/widgets:src/a.go:7', ctx)).toBe(
    '[acme/widgets:src/a.go:7](https://github.com/acme/widgets/blob/HEAD/src/a.go#L7)',
  )
  expect(run('acme/widgets:src/a.go:7:3', known)).toBe(
    '[acme/widgets:src/a.go:7:3](https://git.example/acme/widgets/blob/HEAD/src/a.go#L7)',
  )
  expect(run('acme/widgets:src/a.go:7-9', ctx)).toContain('#L7)')
  // unknown repo: stays plain
  expect(run('other/widgets:src/a.go:7', ctx)).toBe('other/widgets:src/a.go:7')
  expect(run('acme/widgets:src/a.go:7', { cwd: '/w' })).toBe('acme/widgets:src/a.go:7')
})

test('other prefixes are local relative paths', () => {
  expect(run('a/b/c.go:5')).toBe('[a/b/c.go:5](file:///work/proj/a/b/c.go:5)')
  expect(run('acme/other/x.go:5')).toBe('[acme/other/x.go:5](file:///work/proj/acme/other/x.go:5)')
  expect(run('acme/widgets/x.go:5')).toBe('[acme/widgets/x.go:5](file:///work/proj/acme/widgets/x.go:5)')
})

test('text that is not a file reference stays plain', () => {
  for (const s of [
    '12:30',
    'meet at 9:05:10',
    'http://host:80',
    'api.example.com:8080',
    '10.0.0.1:443',
    '[a.go:3](https://h/x)',
    'https://h/foo/bar#12',
    'owner/repo#123',
    'foo.go',
    'foo.go:',
    'xfoo.go:3x',
    'localhost:3000',
    'v1.2.3:4',
  ]) {
    expect(run(s)).toBe(s)
  }
})

test('a token inside a code span stays plain', () => {
  expect(run('use `foo.go:42` here')).toBe('use `foo.go:42` here')
})

test('jira and file-ref in one rule set give the same result in either order', () => {
  const jira: LinkRule = {
    kind: 'jira',
    name: 'jira',
    options: { baseUrl: 'https://tracker.example/browse', keys: ['ABC'] },
  }
  const want = '[src/ABC-12.go:5](file:///work/proj/src/ABC-12.go:5)'
  resetLinkMemo()
  expect(rewriteLinks('src/ABC-12.go:5', [jira, rule()], ctx)).toBe(want)
  resetLinkMemo()
  expect(rewriteLinks('src/ABC-12.go:5', [rule(), jira], ctx)).toBe(want)
})

// Guard for the engine's module loader: a hooks module may import only its own files by relative
// path and the engine module, nothing else. A single `node:` import in one production file makes the
// engine refuse the WHOLE module ("hooks module did not load"), which removes the band and every
// link rule from every session, while every unit test still passes (they run under plain node).
import { expect, test } from 'claude-code/testing'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// Built, not written, so the check's rewrite of the engine module name leaves this file alone.
const ENGINE = ['claude', 'code'].join('-')
// The check copies a shim next to the production files and points the engine import at it.
const SHIM = './shim.mjs'

const SPECIFIER = /(?:^|[\s;(])(?:import|export)\b[^'"`;]*?\bfrom\s*['"]([^'"]+)['"]|(?:^|[\s;(])import\s*['"]([^'"]+)['"]|\b(?:require|import)\s*\(\s*['"]([^'"]+)['"]\s*\)/gm

export function specifiersOf(source: string): string[] {
  const out: string[] = []
  for (const m of source.matchAll(SPECIFIER)) out.push(m[1] ?? m[2] ?? m[3])
  return out
}

// Whole-line comments are not imports: drop them so prose that quotes an import cannot fail the guard.
const withoutCommentLines = (source: string): string =>
  source
    .split('\n')
    .filter(line => !/^\s*(\/\/|\/\*|\*)/.test(line))
    .join('\n')

export function forbiddenIn(source: string): string[] {
  return specifiersOf(withoutCommentLines(source)).filter(s => !(s.startsWith('./') || s.startsWith('../') || s === ENGINE || s === SHIM))
}

const dir = fileURLToPath(new URL('.', import.meta.url))
const production = readdirSync(dir).filter(
  (f: string) => f.endsWith('.ts') && !f.endsWith('.test.ts') && f !== 'testkit.ts' && f !== 'shim.mjs',
)

test('the scan sees the production files it is supposed to guard', () => {
  expect(production.includes('register.ts')).toBe(true)
  expect(production.includes('links.ts')).toBe(true)
  expect(production.length > 20).toBe(true)
})

test('the scan flags a node import, a bare package, a dynamic import and a require', () => {
  expect(forbiddenIn("import { tmpdir } from 'node:os'\n")).toEqual(['node:os'])
  expect(forbiddenIn("import fs from 'fs'\n")).toEqual(['fs'])
  expect(forbiddenIn("export { x } from 'lodash'\n")).toEqual(['lodash'])
  expect(forbiddenIn("const m = await import('node:path')\n")).toEqual(['node:path'])
  expect(forbiddenIn("const m = require('node:fs')\n")).toEqual(['node:fs'])
  expect(forbiddenIn("import 'side-effect'\n")).toEqual(['side-effect'])
})

test('the scan accepts relative paths and the engine module', () => {
  expect(forbiddenIn("import { a } from './a'\nimport type { T } from '../types/pgd'\n")).toEqual([])
  expect(forbiddenIn(`import { atom } from '${ENGINE}'\n`)).toEqual([])
  expect(forbiddenIn("// import x from 'node:os' quoted in a comment is not an import\n")).toEqual([])
})

test('no production hook file imports anything the engine loader refuses', () => {
  const bad: string[] = []
  for (const f of production) {
    for (const s of forbiddenIn(readFileSync(dir + f, 'utf8'))) bad.push(`${f}: ${s}`)
  }
  expect(bad).toEqual([])
})

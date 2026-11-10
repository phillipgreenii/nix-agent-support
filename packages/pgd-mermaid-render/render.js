'use strict'
// pgd-mermaid-render: render one mermaid fence to the SVG file the pgd render hook links to.
// The output name is the 32-bit FNV-1a hash of the exact fence text (opening line through closing
// line), the same function as mermaidHash in claude-marketplace/pgd/hooks/mermaid.ts.
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { spawnSync } = require('node:child_process')

function mermaidHash(source) {
  let h = 0x811c9dc5
  for (let i = 0; i < source.length; i++) {
    h ^= source.charCodeAt(i)
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return h.toString(16).padStart(8, '0')
}

// Mirrors the hook's default: `${tmpdir()}/pgd-mermaid`.
function defaultDir() {
  return path.join(os.tmpdir(), 'pgd-mermaid')
}

function fenceBody(fence) {
  const lines = fence.replace(/\r\n/g, '\n').split('\n')
  const open = /^ {0,3}(`{3,}|~{3,})[ \t]*mermaid/i.exec(lines[0] || '')
  if (!open) throw new Error('input does not start with a mermaid fence')
  let end = lines.length
  while (end > 1 && lines[end - 1].trim() === '') end--
  if (end > 1 && /^ {0,3}(`{3,}|~{3,})[ \t]*$/.test(lines[end - 1])) end--
  return lines.slice(1, end).join('\n') + '\n'
}

function usage() {
  process.stderr.write(
    'usage: pgd-mermaid-render [--out-dir DIR] [--print-path] [--bare] FILE|-\n' +
      '  FILE holds one closed mermaid fence (the exact text the pgd hook claimed); with --bare it\n' +
      '  holds bare diagram source and the hash is taken over that text instead.\n' +
      '  Writes DIR/<fnv1a-8hex>.svg (default DIR: $TMPDIR/pgd-mermaid) and prints the path.\n' +
      '  --print-path computes the path without rendering. PUPPETEER_EXECUTABLE_PATH selects Chrome.\n',
  )
  process.exit(2)
}

const args = process.argv.slice(2)
let dir = defaultDir()
let printOnly = false
let bare = false
let input
for (let i = 0; i < args.length; i++) {
  const a = args[i]
  if (a === '--out-dir') dir = args[++i] || usage()
  else if (a === '--print-path') printOnly = true
  else if (a === '--bare') bare = true
  else if (a === '-h' || a === '--help') usage()
  else if (input === undefined) input = a
  else usage()
}
if (input === undefined) usage()

const text = fs.readFileSync(input === '-' ? 0 : input, 'utf8')
// the hook hashes the fence text without a trailing newline
const hashed = bare ? text : text.replace(/\n+$/, '')
const body = bare ? text : fenceBody(hashed)
const out = path.join(dir, mermaidHash(hashed) + '.svg')
if (printOnly) {
  process.stdout.write(out + '\n')
  process.exit(0)
}

fs.mkdirSync(dir, { recursive: true })
const work = fs.mkdtempSync(path.join(os.tmpdir(), 'pgd-mermaid-src-'))
try {
  const src = path.join(work, 'diagram.mmd')
  fs.writeFileSync(src, body)
  const res = spawnSync(process.env.PGD_MMDC || 'mmdc', ['-q', '-i', src, '-o', out], { stdio: 'inherit' })
  if (res.status !== 0) process.exit(res.status || 1)
} finally {
  fs.rmSync(work, { recursive: true, force: true })
}
process.stdout.write(out + '\n')

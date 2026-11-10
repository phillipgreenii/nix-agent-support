# pgd-mermaid-render

Out-of-band renderer for the mermaid fences the pgd render hook claims
(`claude-marketplace/pgd/hooks/mermaid.ts`). The hook keeps the fence source visible and appends a
link to `file://<tmpdir>/pgd-mermaid/<fnv1a-8hex>.svg`; this tool writes that file.

````bash
pgd-mermaid-render fence.md            # fence.md holds one closed ```mermaid fence
pgd-mermaid-render --print-path fence.md   # print the target path only, no render
pgd-mermaid-render --bare diagram.mmd  # bare diagram source (hash over that text)
````

## Output directory

`$TMPDIR/pgd-mermaid` (Node `os.tmpdir()`, the same expression the hook's `mermaidFileUrl` uses), file
name `<fnv1a-8hex>.svg` where the hash is the 32-bit FNV-1a of the exact fence text (opening line
through closing line, no trailing newline), the same function as `mermaidHash` in the hook. Both
sides MUST stay in step: `--out-dir DIR` overrides the directory for a hook configured with another one.

## Browser

nixpkgs `mermaid-cli` bundles no browser on darwin (chromium is Linux-only). The wrapper sets
`PUPPETEER_EXECUTABLE_PATH` to `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome` when it
is unset; export the variable to use another Chrome. Nothing is installed or downloaded.

## Measurements (aarch64-darwin, 2026-11-10)

See the commit and bead report; the closure is the cached nixpkgs `mermaid-cli` 11.12.0 plus a tiny
wrapper (substituted from cache.nixos.org, nothing built from source).

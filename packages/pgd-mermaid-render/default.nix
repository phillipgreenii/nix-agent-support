# pgd-mermaid-render: out-of-band renderer for the mermaid fences the pgd render hook claims.
# nixpkgs mermaid-cli bundles no browser on darwin (chromium is Linux-only), so the wrapper points
# puppeteer at an installed system Chrome; nothing is downloaded or installed.
{
  lib,
  stdenv,
  writeShellApplication,
  mermaid-cli,
  nodejs,
  runCommand,
}:
let
  render = ./render.js;
  # Default Chrome per platform; override at run time with PUPPETEER_EXECUTABLE_PATH.
  defaultChrome =
    if stdenv.hostPlatform.isDarwin then
      "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
    else
      "";
  pkg = writeShellApplication {
    name = "pgd-mermaid-render";
    runtimeInputs = [
      mermaid-cli
      nodejs
    ];
    text = ''
      if [ -z "''${PUPPETEER_EXECUTABLE_PATH:-}" ]; then
        PUPPETEER_EXECUTABLE_PATH=${lib.escapeShellArg defaultChrome}
        export PUPPETEER_EXECUTABLE_PATH
      fi
      exec node ${render} "$@"
    '';
  };
in
pkg
// {
  # Hash parity with mermaidHash in claude-marketplace/pgd/hooks/mermaid.ts and the path
  # contract, without a browser (so it is hermetic): --print-path only.
  check =
    runCommand "test-pgd-mermaid-render"
      {
        nativeBuildInputs = [ pkg ];
      }
      ''
        export TMPDIR=/tmp/x
        printf '%s\n' '```mermaid' 'graph TD' '  A --> B' '```' > fence.md
        got=$(pgd-mermaid-render --print-path fence.md)
        want=/tmp/x/pgd-mermaid/eca35e29.svg
        [ "$got" = "$want" ] || { echo "got $got want $want" >&2; exit 1; }
        got=$(pgd-mermaid-render --out-dir /o --print-path fence.md)
        [ "$got" = /o/eca35e29.svg ] || { echo "got $got" >&2; exit 1; }
        touch $out
      '';
}

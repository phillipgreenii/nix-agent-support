{ pkgs }:
# Phase-0 stub. Will be rewritten in Task 3 to call mkPgiiPack.
pkgs.runCommand "pgii-pack-test-fixture-0.1.0" { nativeBuildInputs = [ pkgs.envsubst ]; } ''
  cp -R ${./pack-src}/. $out/
  chmod -R u+w $out
  export SCRIPTS_DIR="$out/scripts"
  while IFS= read -r -d "" f; do
    envsubst < "$f" > "''${f%.template}"
    rm "$f"
  done < <(find $out -name "*.template" -print0)
  mkdir -p $out/formulas $out/agents $out/orders $out/scripts
  chmod +x $out/scripts/*.sh
  test -f $out/pack.toml
''

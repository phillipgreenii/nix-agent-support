# mkPgiiPack — generic builder for gascity packs in nix-agent-support.
#
# Takes a pack-src/ directory and produces a derivation whose $out matches
# the layout gascity expects: pack.toml + agents/ + orders/ + scripts/ +
# formulas/ + doctor/ (any of which may be empty).
#
# Template substitution: files ending `.template` are processed by envsubst
# using `${KEY}` markers. Only declared variables are substituted (we pass
# envsubst an explicit variable list); other `${...}` patterns in template
# files (e.g. shell expansions inside *.sh.template) are preserved verbatim.
# `${SCRIPTS_DIR}` is always available and resolves to the pack's scripts/
# subdir inside the nix store. Additional substitutions come from the
# `substitutions` argument (an attrset of NAME=value pairs).
#
# Usage:
#
#   { lib, mkPgiiPack }:
#   mkPgiiPack {
#     name = "pgii-pack-foo";
#     src = ./pack-src;
#     substitutions = {
#       SOURCES_JSON = builtins.toJSON sources;
#     };
#   }
{ lib, pkgs }:
{
  name,
  version ? "0.1.0",
  src,
  substitutions ? { },
  meta ? { },
}:
let
  # Infer pack scope from pack.toml's first [[named_session]] entry. This is
  # the same field gascity reads to decide where to bind the session
  # template. Packs with no [[named_session]] block default to "city" — they
  # contribute orders, scripts, or doctor checks, not session templates.
  packToml = builtins.fromTOML (builtins.readFile (src + "/pack.toml"));
  sessions =
    if packToml ? named_session then
      (
        if builtins.isList packToml.named_session then
          packToml.named_session
        else
          [ packToml.named_session ]
      )
    else
      [ ];
  firstScoped = lib.findFirst (s: s ? scope) null sessions;
  scope = if firstScoped == null then "city" else firstScoped.scope;
in
pkgs.runCommand "${name}-${version}"
  {
    passthru = { inherit name; };
    nativeBuildInputs = [ pkgs.envsubst ];
    inherit meta;
  }
  ''
    cp -R ${src}/. $out/
    chmod -R u+w $out

    # ''${PACK_ROOT} always points at this pack's root in the nix store.
    # ''${SCRIPTS_DIR} always points at this pack's scripts/ subdir.
    export PACK_ROOT="$out"
    export SCRIPTS_DIR="$out/scripts"
    ${lib.concatStringsSep "\n" (
      lib.mapAttrsToList (k: v: "export ${k}=${lib.escapeShellArg (toString v)}") substitutions
    )}

    # Substitute. envsubst replaces every ''${VAR} in the input file with
    # the corresponding environment-variable value, IF the variable is
    # exported. Unexported names pass through unchanged, so accidental
    # collisions with shell ''${...} inside *.sh.template files are limited
    # to names a pack author also chose to declare in `substitutions`.
    # If a pack ever needs envsubst to ignore certain names, refactor
    # this to use envsubst's variable-list arg (passed as a single
    # argument like '$SCRIPTS_DIR $REPOS') at that time.
    while IFS= read -r -d "" f; do
      envsubst < "$f" > "''${f%.template}"
      rm "$f"
    done < <(find $out -name "*.template" -print0)

    mkdir -p $out/formulas $out/agents $out/orders $out/scripts $out/doctor

    if [ -d $out/scripts ] && compgen -G "$out/scripts/*.sh" > /dev/null; then
      chmod +x $out/scripts/*.sh
    fi

    test -f $out/pack.toml || { echo "mkPgiiPack: missing pack.toml in ${name}" >&2; exit 1; }
    if find $out -name "*.template" | grep -q .; then
      echo "mkPgiiPack: unsubstituted .template files remain in ${name}" >&2
      find $out -name "*.template" >&2
      exit 1
    fi

    cat > $out/.pack-meta.json <<EOF
    { "name": "${name}", "version": "${version}", "scope": "${scope}" }
    EOF
  ''

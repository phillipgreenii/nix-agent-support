#!/usr/bin/env bash
# Assert that each Go module's gomod2nix.toml describes ONLY that module's own
# dependency graph, as `gomod2nix generate --with-deps` writes it with GOWORK=off.
#
# Why: a toml regenerated while the repository-root go.work is in effect walks the
# whole workspace's `all` pattern, so it lists other workspace modules' packages
# (even their `main` packages) in cachePackages and their dependencies under [mod].
# mkGoApp drops only LOCAL-REPLACE modules from the cache roots
# (`phillipg-nix-repo-base` lib/go-builders.nix, localReplaceMods), so any other
# first-party cache entry fails the go-cache-env build with
# "import lookup disabled by -mod=vendor".
#
# Two rules per module:
#   1. A cachePackages entry inside a workspace module (any packages/*/go.mod
#      `module` path, matched as the path itself or the path plus "/") MUST lie
#      inside a module this go.mod replaces with a local path (`=> ../x` or
#      `=> ./x`, single-line or `replace ( ... )` block). The module's own
#      packages are NOT allowed: the builder does not drop them either.
#   2. Every [mod] entry MUST be a module named in this module's go.sum.
#   3. Staleness (the scheduled refresh no longer regenerates modules that get no
#      `go get -u`, so this is what catches a go.mod/go.sum edit that left the toml
#      behind): every [mod] entry's version MUST be a version go.sum holds a source
#      hash (`h1:`) for, and where go.mod requires a module at a version, the toml
#      entry for that module (when present) MUST carry that same version.
#
# The toml is read in gomod2nix's fixed output layout (`cachePackages = [`, one
# quoted entry per line, `]`; `[mod.'path']` headers), so no TOML tool is needed
# and the check runs anywhere bash, sed, awk and sort exist.
#
# Usage: check-gomod2nix-cache-scope.sh <repo-root> [module-dir-name...]
#   No module names: check every packages/*/gomod2nix.toml.
# Exit: 0 every checked module is in scope; 3 violations found (each listed on
# stderr); 2 usage error, unknown module or no Go modules under the root;
# 1 unexpected failure.
set -euo pipefail

usage() {
  echo "usage: check-gomod2nix-cache-scope.sh <repo-root> [module-dir-name...]" >&2
  exit 2
}

[ "$#" -ge 1 ] || usage
root="$1"
shift

if [ ! -d "${root}/packages" ]; then
  echo "check-gomod2nix-cache-scope: no packages/ directory under ${root} (wrong root?)" >&2
  exit 2
fi

# module_path <go.mod>: the `module` directive's path
module_path() {
  sed -nE 's/^[[:space:]]*module[[:space:]]+([^[:space:]]+)[[:space:]]*$/\1/p' "$1" | head -n 1
}

# replace_targets <go.mod>: module paths replaced by a local path. The SAME
# whole-line pattern as the builder's localReplaceMods, so both agree on what
# counts (a prose comment that mentions "replace => ../x" does not).
replace_targets() {
  sed -nE 's/^[[:space:]]*(replace[[:space:]]+)?([^[:space:]]+)([[:space:]]+[^[:space:]]+)?[[:space:]]+=>[[:space:]]+\.\.?\/.*$/\2/p' "$1"
}

# cache_entries <toml>: the quoted entries of the cachePackages array
cache_entries() {
  awk '
    /^cachePackages[[:space:]]*=[[:space:]]*\[/ { in_list = 1 }
    in_list {
      line = $0
      while (match(line, /'"'"'[^'"'"']+'"'"'|"[^"]+"/)) {
        print substr(line, RSTART + 1, RLENGTH - 2)
        line = substr(line, RSTART + RLENGTH)
      }
      if ($0 ~ /\]/) { in_list = 0 }
    }
  ' "$1"
}

# mod_entries <toml>: the module paths of the [mod.'path'] tables
mod_entries() {
  sed -nE "s/^[[:space:]]*\[mod\.['\"]([^'\"]+)['\"]\][[:space:]]*$/\1/p" "$1"
}

# mod_versions <toml>: "<path> <version>" for each [mod.'path'] table
mod_versions() {
  awk '
    /^[[:space:]]*\[mod\./ {
      p = $0; sub(/^[[:space:]]*\[mod\.[\x27"]/, "", p); sub(/[\x27"]\].*$/, "", p); path = p; next
    }
    path != "" && /^[[:space:]]*version[[:space:]]*=/ {
      v = $0; sub(/^[^=]*=[[:space:]]*[\x27"]/, "", v); sub(/[\x27"].*$/, "", v)
      print path, v; path = ""
    }
  ' "$1"
}

# require_versions <go.mod>: "<path> <version>" for each requirement
require_versions() {
  awk '
    /^[[:space:]]*require[[:space:]]*\(/ { blk = 1; next }
    blk && /^[[:space:]]*\)/ { blk = 0; next }
    blk { sub(/\/\/.*/, ""); if (NF >= 2) print $1, $2; next }
    /^[[:space:]]*require[[:space:]]+[^(]/ { sub(/\/\/.*/, ""); print $2, $3 }
  ' "$1"
}

# under <path> <prefix>: path is prefix itself or inside it
under() { [ "$1" = "$2" ] || [ "${1#"$2"/}" != "$1" ]; }

# Every workspace module path (from every go.mod, whether or not it has a toml).
declare -A workspace=()
for gomod in "${root}"/packages/*/go.mod; do
  [ -e "${gomod}" ] || continue
  path="$(module_path "${gomod}")"
  if [ -z "${path}" ]; then
    echo "check-gomod2nix-cache-scope: ${gomod#"${root}"/} has no module directive" >&2
    exit 2
  fi
  workspace["${path}"]=1
done

if [ "$#" -gt 0 ]; then
  modules=("$@")
else
  mapfile -t modules < <(
    for toml in "${root}"/packages/*/gomod2nix.toml; do
      [ -e "${toml}" ] || continue
      basename "$(dirname "${toml}")"
    done | sort
  )
fi
if [ "${#modules[@]}" -eq 0 ]; then
  echo "check-gomod2nix-cache-scope: found no packages/*/gomod2nix.toml under ${root} (wrong root?)" >&2
  exit 2
fi

problems=()
for mod in "${modules[@]}"; do
  dir="${root}/packages/${mod}"
  if [ ! -f "${dir}/gomod2nix.toml" ] || [ ! -f "${dir}/go.mod" ]; then
    echo "check-gomod2nix-cache-scope: unknown module '${mod}' (no packages/${mod}/go.mod and gomod2nix.toml)" >&2
    exit 2
  fi

  mapfile -t allowed < <(replace_targets "${dir}/go.mod")

  while IFS= read -r entry; do
    [ -n "${entry}" ] || continue
    owner=""
    for ws in "${!workspace[@]}"; do
      if under "${entry}" "${ws}" && [ "${#ws}" -gt "${#owner}" ]; then
        owner="${ws}"
      fi
    done
    [ -n "${owner}" ] || continue
    ok=false
    for r in "${allowed[@]}"; do
      if under "${entry}" "${r}"; then
        ok=true
        break
      fi
    done
    if [ "${ok}" = false ]; then
      problems+=("${mod}: cachePackages entry ${entry} belongs to workspace module ${owner}, which packages/${mod}/go.mod does not replace with a local path")
    fi
  done < <(cache_entries "${dir}/gomod2nix.toml")

  declare -A summed=() summedver=() required=()
  if [ -f "${dir}/go.sum" ]; then
    while read -r m v _; do
      [ -n "${m}" ] && summed["${m}"]=1
      # A source hash line is `path vX h1:...`; the `vX/go.mod` lines are not it.
      case "${v}" in
      */go.mod) ;;
      *) [ -n "${v}" ] && summedver["${m} ${v}"]=1 ;;
      esac
    done <"${dir}/go.sum"
  fi
  # go.mod requirements (single-line and block form).
  while read -r m v; do
    [ -n "${m}" ] && required["${m}"]="${v}"
  done < <(require_versions "${dir}/go.mod")
  while IFS= read -r m; do
    [ -n "${m}" ] || continue
    if [ -z "${summed[${m}]:-}" ]; then
      problems+=("${mod}: [mod] entry ${m} is not in go.sum")
    fi
  done < <(mod_entries "${dir}/gomod2nix.toml")
  while read -r m v; do
    [ -n "${m}" ] || continue
    if [ -n "${summed[${m}]:-}" ] && [ -z "${summedver["${m} ${v}"]:-}" ]; then
      problems+=("${mod}: stale gomod2nix.toml: [mod] entry ${m} is at ${v}, which go.sum has no source hash for")
    elif [ -n "${required[${m}]:-}" ] && [ "${required[${m}]}" != "${v}" ]; then
      problems+=("${mod}: stale gomod2nix.toml: [mod] entry ${m} is at ${v} but go.mod requires ${required[${m}]}")
    fi
  done < <(mod_versions "${dir}/gomod2nix.toml")
  unset summed summedver required
done

if [ "${#problems[@]}" -gt 0 ]; then
  echo "FAIL: gomod2nix.toml lists entries outside its module's own dependency graph (${#problems[@]} problem(s)):" >&2
  printf '  - %s\n' "${problems[@]}" >&2
  echo "Likely cause: it was generated with a go.work in effect. Regenerate with GOWORK=off by running packages/<module>/update-deps.sh." >&2
  exit 3
fi

plural="s"
[ "${#modules[@]}" -eq 1 ] && plural=""
echo "OK: ${#modules[@]} module${plural}: gomod2nix.toml entries are within each module's own dependency graph."

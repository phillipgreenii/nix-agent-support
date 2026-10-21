# Shared helper for the two "temp root" home-manager modules
# (home/programs/build-tmp and home/programs/claude-code-tmp, bead pg2-2qirl,
# packet A of the build_tmp design). Both modules are the same shape, so the
# common parts live here once:
#
#   * the option set (enable, path with NO default, the two idle-time options
#     that packet B's registry items read),
#   * the activation step that creates the directory (0700) plus its marker,
#   * the guarded shell snippet that exports the environment variables,
#   * the CETA contribution (the root as BOTH a temp root and a read-write
#     root) and the pg-disk-reclaimer registry-dependency assertion.
#
# `mkModule` returns the module body for a module of the form
#
#   { config, lib, options, ... }:
#   (import ../../../lib/tmp-root.nix { inherit lib; }).mkModule { ... } { inherit config options; }
#
# Pure functions below (`mkActivationScript`, `mkEnvSnippet`) are exposed so the
# tests can render and RUN the text they produce.
{ lib }:
let
  # Collapse repeated and trailing slashes: "/a//b/" -> "/a/b". Only applied to
  # an absolute path; a relative one is left alone (and trips the absolute-path
  # assertion) rather than being silently turned into an absolute one.
  normalizePath =
    p:
    if lib.hasPrefix "/" p then
      "/" + lib.concatStringsSep "/" (lib.filter (x: x != "") (lib.splitString "/" p))
    else
      p;

  # The activation step's script TEXT (run by home-manager's activation after
  # `writeBoundary`; `$DRY_RUN_CMD` is home-manager's dry-run prefix and is empty
  # in a real apply). Contract:
  #   * the parent directory (the volume's mount point) MUST already exist; the
  #     mount point cannot be created by a user, so a missing parent is a skip;
  #   * a symlink at the target is refused;
  #   * otherwise mkdir -p, chmod 0700 (idempotent, on EVERY apply) and write the
  #     marker the sweep's root guard demands;
  #   * every outcome exits 0: a failure prints a skip message instead, so a
  #     `set -e` activation cannot abort the whole apply.
  # The work runs in a subshell so none of its variables leak into the shared
  # activation scope.
  mkActivationScript =
    {
      label,
      path,
      marker,
    }:
    ''
      (
        root=${lib.escapeShellArg path}
        parent="$(dirname "$root")"
        if [ ! -d "$parent" ]; then
          echo "${label}: skipping, parent directory $parent does not exist (volume not mounted?)"
          exit 0
        fi
        if [ -L "$root" ]; then
          echo "${label}: refusing, $root is a symlink"
          exit 0
        fi
        $DRY_RUN_CMD mkdir -p "$root" \
          && $DRY_RUN_CMD chmod 0700 "$root" \
          && $DRY_RUN_CMD touch "$root/${marker}"
      ) || echo "${label}: could not prepare ${path}; skipped"
    '';

  # The shell snippet that exports `vars` (each set to the root) ONLY when the
  # directory exists and is writable at shell start; otherwise it exports
  # nothing, so a shell started without the volume keeps its old environment.
  # Plain POSIX `[ ]` and `export`, so the same text is valid in bash and zsh,
  # and it defines no function or variable that could leak into the user's shell.
  # It deliberately never mentions CETA_EXTRA_TEMP_ROOTS: CETA learns the roots
  # through nix (ceta's extraTempRoots option), not through the shell.
  mkEnvSnippet =
    {
      label,
      path,
      vars,
    }:
    let
      root = lib.escapeShellArg path;
    in
    ''
      # ${label} (pg2-2qirl): export only when the directory exists and is writable.
      if [ -d ${root} ] && [ -w ${root} ] && [ -x ${root} ]; then
      ${lib.concatMapStringsSep "\n" (v: "  export ${v}=${root}") vars}
      fi
    '';

  mkModule =
    {
      # option name under phillipgreenii.programs, e.g. "build-tmp"
      name,
      # required last path component, e.g. "build_tmp"
      basename,
      # the marker file written into the root, e.g. ".build-tmp-root"
      marker,
      # camelCase home.activation entry name
      activationName,
      # one-line human description of what the root holds
      summary,
      # variables the root is exported as, given the module's own cfg
      envVars,
      # extra options (e.g. exportTmpdir) merged into the option set
      extraOptions ? { },
      level1Default,
      level3Default,
      level1Doc,
      level3Doc,
    }:
    { config, options }:
    let
      cfg = config.phillipgreenii.programs.${name};
      programs = options.phillipgreenii.programs;

      # Read from the `options` argument, never from `config`/`pkgs` (using those
      # to choose attribute NAMES is infinite recursion; see home/programs/pa-monitor).
      hasCeta = programs ? claude-extended-tool-approver;
      hasReclaimer = programs ? pg-disk-reclaimer;

      rootPath = normalizePath cfg.path;
      segs = lib.splitString "/" cfg.path;
      prefix = "phillipgreenii.programs.${name}";

      snippet = mkEnvSnippet {
        label = name;
        path = rootPath;
        vars = envVars cfg;
      };
    in
    {
      options.phillipgreenii.programs.${name} = {
        enable = lib.mkEnableOption "the ${name} root: ${summary}";
        path = lib.mkOption {
          type = lib.types.str;
          example = "/Volumes/data/${basename}";
          description = ''
            Absolute path of the ${name} root, whose last component MUST be
            `${basename}`. NO default: the path is machine-specific (it names a
            volume) and a generic module never carries one. The directory is created
            (mode 0700) by the activation step when its parent directory (the
            volume's mount point) already exists. Required when `enable` is set.
          '';
        };
        level1Minutes = lib.mkOption {
          type = lib.types.ints.positive;
          default = level1Default;
          description = "Idle time in minutes of the aggressiveness-1 cleanup variant. ${level1Doc}";
        };
        level3Minutes = lib.mkOption {
          type = lib.types.ints.positive;
          default = level3Default;
          description = "Idle time in minutes of the aggressiveness-3 cleanup variant. ${level3Doc}";
        };
      }
      // extraOptions;

      config = lib.mkMerge [
        (lib.mkIf cfg.enable {
          assertions = [
            {
              assertion = lib.hasPrefix "/" cfg.path;
              message = "${prefix}.path (\"${cfg.path}\") must be an absolute path.";
            }
            {
              assertion = !(lib.elem ".." segs || lib.elem "." segs);
              message = "${prefix}.path (\"${cfg.path}\") must not contain a '.' or '..' segment.";
            }
            {
              assertion = builtins.baseNameOf cfg.path == basename;
              message = "${prefix}.path (\"${cfg.path}\") must end in \"${basename}\": the sweep refuses any other basename.";
            }
            {
              # The registry items (packet B) are only written when pg-disk-reclaimer
              # is enabled; without it the daily sweep would run against a missing
              # registry and the root would grow without bound.
              assertion = hasReclaimer && config.phillipgreenii.programs.pg-disk-reclaimer.enable;
              message = "${prefix}.enable requires phillipgreenii.programs.pg-disk-reclaimer.enable = true (and its module imported): the registry that the cleanup of ${rootPath} is driven by is only written when it is enabled.";
            }
          ];

          home.activation.${activationName} = lib.hm.dag.entryAfter [ "writeBoundary" ] (mkActivationScript {
            label = name;
            path = rootPath;
            inherit marker;
          });

          # Gated on the HM shell flags. envExtra is .zshenv (every zsh); for bash
          # the same text goes to the profile and rc extras. Not home.sessionVariables:
          # that is static and cannot be guarded by existence. initExtra/initContent
          # are interactive-only and are NOT used. Limitation: GUI/launchd-started
          # processes do not read these files.
          programs = {
            zsh.envExtra = lib.mkIf config.programs.zsh.enable snippet;
            bash = {
              profileExtra = lib.mkIf config.programs.bash.enable snippet;
              bashrcExtra = lib.mkIf config.programs.bash.enable snippet;
            };
          };
        })

        # CETA contribution, BOTH facets (design D12): the root is TEMP (the
        # temp-repo carve-out, Delete) and READ-WRITE (path-safety; temp
        # classification alone gives no read/write opinion). Contributed only when
        # CETA's module is imported (its options declared) and this root is
        # enabled; optionalAttrs because a definition of an undeclared option
        # errors even under mkIf false. List options merge with consumer values.
        (lib.optionalAttrs hasCeta {
          phillipgreenii.programs.claude-extended-tool-approver = lib.mkIf cfg.enable {
            extraTempRoots = [ rootPath ];
            extraReadWriteRoots = [ rootPath ];
          };
        })
      ];
    };
in
{
  inherit
    normalizePath
    mkActivationScript
    mkEnvSnippet
    mkModule
    ;
}

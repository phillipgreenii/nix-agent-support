# claude-hook-router — HM wiring for ADR 0071's Claude Code hook router (Phase C1).
#
# Owns the SHARED registration point first-party hook delegates contribute to
# (`programs.claude-hook-router.delegates`, ADR 0071 §2.2), renders that fully-merged
# list into `hooks/hooks.json` + `router-config.json` at HM build time, builds the
# router plugin via `phillipg-nix-repo-base`'s `mkClaudeHookRouterPlugin` (packet A1),
# registers it through the existing `marketplaces.nixProvided` pipeline every other
# in-repo plugin already uses (`home/programs/claude-marketplaces`), and puts packet
# B1's router binary on `home.packages`, co-gated on `claude-code.enable` (matching
# `claude-extended-tool-approver`'s own precedent).
#
# Why this module renders hooks.json/router-config.json ITSELF rather than feeding
# `cfg.delegates` through `mkClaudeHookRouterPlugin`'s own `sources` argument: that
# argument merges DISTINCT, uniquely-named THIRD-PARTY plugin directories (ADR 0071
# §2.2's second bullet, §2.6) — every hook entry a `sources` element contributes is
# stamped with that ONE source's own `name`/`priority` uniformly
# (`claude-marketplace.nix`'s `readSourceDelegates`: `inherit (s) name priority;`).
# First-party `delegates` entries are the OPPOSITE shape: ADR 0071 §1.3 confirms one
# contributing tool ("ceta as a whole is one delegate") registers MULTIPLE entries
# sharing the SAME `name` across its different events (packet C2 migrates ceta's real
# five-event surface), each with its OWN `priority`. Feeding that through `sources`
# (which requires unique source names and collapses every hook from one source to a
# single priority) would either throw on the very first multi-event first-party
# contributor or silently discard per-delegate ordering — so `mkClaudeHookRouterPlugin`
# is used here only for the base plugin skeleton (`sources = [ ]`, giving proper
# `<declared>+<digest>` version stamping matching every other in-repo plugin), and this
# module overlays the actual `hooks/hooks.json` + `router-config.json` it renders
# directly from `cfg.delegates` — exactly what ADR 0071 §2.2 states this module does:
# "The router's HM module reads the fully-merged option at HM build time and renders
# both its own `hooks.json` entries … and a static, ordered `router-config.json`."
{
  config,
  lib,
  pkgs,
  inputs,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude-hook-router;

  marketplaceBuilders = inputs.phillipgreenii-nix-base.lib.mkClaudeMarketplaceBuilders {
    inherit pkgs lib;
  };

  routerCommand = "claude-hook-router";

  # ADR 0071 §2.4's event -> allowed-contract table, replicated here so a first-party
  # delegate's contract/event combination is validated the same way A1's generator
  # validates a vendored one. An event absent from this table defaults to
  # observe-only, matching the design's own stated conservative default.
  allowedContractsByEvent = {
    PreToolUse = [
      "decide"
      "rewrite"
      "annotate"
      "observe"
      "decide+rewrite"
    ];
    PermissionRequest = [
      "decide"
      "rewrite"
      "annotate"
      "observe"
      "decide+rewrite"
    ];
    PostToolUse = [
      "rewrite"
      "annotate"
      "observe"
    ];
    PermissionDenied = [
      "decide"
      "annotate"
      "observe"
    ];
    SessionEnd = [ "observe" ];
  };
  contractsForEvent = event: allowedContractsByEvent.${event} or [ "observe" ];

  # ADR 0071 §2.3's matcher classification: only letters/digits/_/-/spaces/,/| is
  # exact-string-or-pipe-alternation; absent/null/"*"/"" is match-all; anything else
  # is regex-shaped and needs manual review before being trusted (same rule A1's
  # generator enforces for vendored delegates).
  classifyMatcher =
    m:
    if m == null || m == "" || m == "*" then
      "match-all"
    else if builtins.match "[A-Za-z0-9_, |-]+" m != null then
      "exact"
    else
      "regex";

  delegateSubmodule = lib.types.submodule (_: {
    options = {
      name = lib.mkOption {
        type = lib.types.str;
        description = ''
          Identifies which contributing tool this delegate belongs to (e.g. "ceta").
          Need not be unique across the whole merged list -- one tool contributes one
          entry per hook event it registers for, all sharing this same name (ADR 0071
          §1.3: "ceta as a whole is one delegate").
        '';
      };
      event = lib.mkOption {
        type = lib.types.str;
        description = ''
          Claude Code hook event this delegate registers for (e.g. PreToolUse,
          PostToolUse, PermissionRequest, PermissionDenied, SessionEnd). Every event
          is supported (ADR 0071 §2.1); an event with no delegate gets no hooks.json
          entry at all.
        '';
      };
      matcher = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          Exact-string/pipe-alternation matcher (e.g. "Bash", "Write|Edit"). Null
          (the default) means match-all for this delegate. A regex-shaped matcher
          (anything outside letters/digits/_/-/spaces/,/|) is rejected at build time
          for manual review (ADR 0071 §2.3).
        '';
      };
      command = lib.mkOption {
        type = lib.types.str;
        description = ''
          Bare command the router invokes for this delegate -- never a plugin-relative
          path, matching the ceta/pg-pr precedent (ADR 0071 §2.7): the command must
          resolve on PATH, typically via this delegate's own `home.packages` entry.
        '';
      };
      contract = lib.mkOption {
        type = lib.types.enum [
          "decide"
          "rewrite"
          "annotate"
          "observe"
          "decide+rewrite"
        ];
        description = ''
          Dispatch contract this delegate honors for this event (ADR 0071 §2.4).
          Must be one of the contracts that event allows (§2.4's table); an
          unsupported combination is a build-time error.
        '';
      };
      priority = lib.mkOption {
        type = lib.types.int;
        default = 1000;
        description = ''
          Ordering value within this event's delegate list -- ascending, ties broken
          by name (ADR 0071 §2.2/§2.4). This is the literal wire-shape value written
          into router-config.json.

          Per Phase C1's ordering-mechanism correction: this per-ELEMENT field is
          unaffected by the banded convention -- what changes is how a CONTRIBUTING
          module places its list among other contributors. A contributing module
          MUST wrap the WHOLE LIST it assigns to `programs.claude-hook-router.delegates`
          with `lib.mkOrder`/`lib.mkBefore`/`lib.mkAfter` (never plain assignment),
          matching `docs/adr/0020-status-line-parts-ordering-convention.md`'s own
          convention (base band `lib.mkOrder 1000`; `lib.mkBefore` = band 500 for
          delegates that must run first; `lib.mkAfter` = band 1500 for delegates that
          must run last; `lib.mkOrder N` for finer placement) -- by convention, using
          the SAME numeric value for both the list-level band and each element's own
          `priority` field keeps the two mechanisms in agreement.
        '';
      };
    };
  });
in
{
  options.phillipgreenii.programs.claude-hook-router = {
    enable = lib.mkEnableOption "the Claude Code hook router (ADR 0071)";

    package = lib.mkPackageOption pkgs "claude-hook-router" { };

    delegates = lib.mkOption {
      type = lib.types.listOf delegateSubmodule;
      default = [ ];
      description = ''
        Shared registration point for first-party hook delegates (ADR 0071 §2.2). A
        contributing module (e.g. claude-extended-tool-approver) assigns its OWN list
        of `{ name; event; matcher; command; contract; priority; }` entries here,
        wrapped with `lib.mkOrder`/`lib.mkBefore`/`lib.mkAfter` on the WHOLE list it
        assigns (never plain assignment) -- see this option's `priority` sub-field and
        `docs/adr/0020-status-line-parts-ordering-convention.md` for the banded
        convention this replicates.

        This module reads the fully-merged list at HM build time and renders both its
        own `hooks/hooks.json` (one entry per event with >=1 delegate) and a static,
        ordered `router-config.json` the runtime binary reads -- no runtime
        plugin-discovery mechanism is used.
      '';
    };
  };

  config = lib.mkIf (config.phillipgreenii.programs.claude-code.enable && cfg.enable) (
    let
      badContracts = lib.filter (d: !(lib.elem d.contract (contractsForEvent d.event))) cfg.delegates;

      regexMatchers = lib.filter (d: classifyMatcher d.matcher == "regex") cfg.delegates;

      # Group by event, sort each event's list priority-ascending then name (ADR 0071
      # §2.2/§2.4's dispatch order -- same comparator A1's generator uses).
      byEvent = lib.groupBy (d: d.event) cfg.delegates;
      sortDelegates = lib.sort (
        a: b: if a.priority != b.priority then a.priority < b.priority else a.name < b.name
      );
      sortedByEvent = lib.mapAttrs (_event: sortDelegates) byEvent;

      # Broadest matcher needed per event (ADR 0071 §2.3): match-all if any delegate
      # wants it, the shared matcher if every delegate on that event agrees, otherwise
      # match-all as the chosen fallback -- the router's own dispatch loop still
      # filters per delegate at runtime using each delegate's ORIGINAL matcher from
      # router-config.json, so this registration-level union is only ever a coarser
      # pre-filter.
      unionMatcherForEvent =
        delegates:
        let
          matchers = map (d: d.matcher) delegates;
        in
        if lib.any (m: m == null) matchers then
          null
        else if lib.all (m: m == builtins.head matchers) matchers then
          builtins.head matchers
        else
          null;

      hooksJsonEvents = lib.mapAttrs (
        _event: delegates:
        let
          m = unionMatcherForEvent delegates;
          hookCmd = {
            type = "command";
            command = routerCommand;
          };
        in
        [ ({ hooks = [ hookCmd ]; } // (if m == null then { } else { matcher = m; })) ]
      ) sortedByEvent;

      routerConfig = lib.mapAttrs (
        _event: delegates:
        map (d: {
          inherit (d)
            name
            event
            matcher
            command
            contract
            priority
            ;
        }) delegates
      ) sortedByEvent;

      hooksJsonFile = pkgs.writeText "claude-hook-router-hooks.json" (
        builtins.toJSON { hooks = hooksJsonEvents; }
      );
      routerConfigFile = pkgs.writeText "claude-hook-router-router-config.json" (
        builtins.toJSON routerConfig
      );

      # Base plugin skeleton via packet A1's generator (no third-party sources are
      # wired up by this packet -- see this file's header comment for why first-party
      # delegates cannot go through `sources` themselves). This gets us the same
      # `<declared>+<digest>` version stamping and `.claude-plugin/plugin.json` shape
      # every other in-repo plugin has.
      pluginSkeleton = marketplaceBuilders.mkClaudeHookRouterPlugin {
        name = "hook-router";
        declared = "1.0.0";
        description = "Dispatches Claude Code hook events to first-party delegates registered via programs.claude-hook-router.delegates (ADR 0071).";
        inherit routerCommand;
        sources = [ ];
      };

      # Overlay this module's own directly-rendered hooks.json/router-config.json onto
      # the skeleton, and mark the plugin enabled by default (mkClaudeHookRouterPlugin's
      # generated plugin.json carries no `defaultEnabled` key, which mkClaudeMarketplace
      # would otherwise resolve to `false` -- every other in-repo plugin ships
      # `defaultEnabled: true`, and this plugin is only ever present in the marketplace
      # list at all once `cfg.enable` is true).
      pluginDir =
        pkgs.runCommand "claude-hook-router-plugin"
          {
            nativeBuildInputs = [ pkgs.jq ];
          }
          ''
            mkdir -p "$out"
            cp -r ${pluginSkeleton}/. "$out/"
            chmod -R u+w "$out"
            cp ${hooksJsonFile} "$out/hooks/hooks.json"
            cp ${routerConfigFile} "$out/router-config.json"
            jq '.defaultEnabled = true' "$out/.claude-plugin/plugin.json" \
              > "$out/.claude-plugin/plugin.json.tmp"
            mv "$out/.claude-plugin/plugin.json.tmp" "$out/.claude-plugin/plugin.json"
          '';

      # mkClaudeMarketplace needs a source tree carrying its own
      # .claude-plugin/marketplace.json listing the plugin(s) it bundles (the same
      # shape this repo's own committed claude-marketplace/ tree has) -- built here
      # rather than committed, since the router plugin's content is HM-config-dependent
      # (cfg.delegates), unlike every statically-registered plugin under claude-marketplace/.
      marketplaceManifest = pkgs.writeText "claude-hook-router-marketplace.json" (
        builtins.toJSON {
          name = "claude-hook-router";
          owner = {
            name = "phillipgreenii";
          };
          plugins = [
            {
              name = "hook-router";
              source = "./hook-router";
            }
          ];
        }
      );

      marketplaceSrc = pkgs.runCommand "claude-hook-router-marketplace-src" { } ''
        mkdir -p "$out/.claude-plugin" "$out/hook-router"
        cp ${marketplaceManifest} "$out/.claude-plugin/marketplace.json"
        cp -r ${pluginDir}/. "$out/hook-router/"
        chmod -R u+w "$out"
      '';

      marketplace = marketplaceBuilders.mkClaudeMarketplace { src = marketplaceSrc; };
    in
    {
      assertions = [
        {
          assertion = badContracts == [ ];
          message = ''
            phillipgreenii.programs.claude-hook-router.delegates: the following entries
            declare a contract their event does not allow (ADR 0071 §2.4): ${
              lib.concatMapStringsSep ", " (
                d:
                "${d.name}/${d.event} declares '${d.contract}' (allowed: ${lib.concatStringsSep ", " (contractsForEvent d.event)})"
              ) badContracts
            }
          '';
        }
        {
          assertion = regexMatchers == [ ];
          message = ''
            phillipgreenii.programs.claude-hook-router.delegates: the following entries
            have a regex-shaped matcher, which ADR 0071 §2.3 requires be manually
            reviewed before use (only exact-string/pipe-alternation matchers are
            accepted without review): ${
              lib.concatMapStringsSep ", " (d: "${d.name}/${d.event} ('${toString d.matcher}')") regexMatchers
            }
          '';
        }
      ];

      home.packages = [ cfg.package ];

      phillipgreenii.programs.claude-code.marketplaces.nixProvided = [ marketplace ];
    }
  );
}

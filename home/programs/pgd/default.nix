{
  config,
  lib,
  ...
}:

# pgd module: the opt-in options of the display mod
# (claude-marketplace/pgd). The plugin itself is delivered by the
# committed claude-marketplace/ tree (default-enabled); this module renders the
# options as one JSON document in PG_CLAUDE_DISPLAY_OPTIONS (home.sessionVariables),
# which the mod reads at session start. It installs no binary and no settings.
# Defaults match today's shell line: every component on, except beads, which follows
# phillipgreenii.programs.beads.enable.
let
  cfg = config.phillipgreenii.programs.pgd;
  component =
    description:
    lib.mkOption {
      type = lib.types.bool;
      default = true;
      inherit description;
    };
  # One link rule as the mod reads it: null pattern and url are dropped, and kind and settings
  # (rendered as `options`) only when set, so a kind-less entry renders as it always did.
  renderLink =
    l:
    {
      inherit (l) name;
    }
    // lib.optionalAttrs (l.kind != null) { inherit (l) kind; }
    // lib.optionalAttrs (l.pattern != null) { inherit (l) pattern; }
    // lib.optionalAttrs (l.url != null) { inherit (l) url; }
    // lib.optionalAttrs (l.settings != { }) { options = l.settings; };
  rendered = {
    inherit (cfg) enable components github;
    links = map renderLink cfg.links;
    alerts = { inherit (cfg.alerts) thresholds; };
    metrics = lib.optionalAttrs (cfg.metrics.endpoint != null) { inherit (cfg.metrics) endpoint; };
  };
in
{
  options.phillipgreenii.programs.pgd = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Master switch of the display mod. When false the mod registers nothing (no band,
        alert, source or component).
      '';
    };

    components = {
      # Bundle switch (design note docs/superpowers/specs/2026-11-07-mod-bundle-grouping-design.md,
      # "beads"): the beads bundle's pgd component follows
      # phillipgreenii.programs.beads.enable; an explicit setting still wins. `or false`
      # reads a machine that does not declare the beads option as off.
      beads = component "Beads contributions: dolt alert and Focus rows." // {
        default = config.phillipgreenii.programs.beads.enable or false;
      };
      assume = component "Assume button, modal, expiry alert and source.";
      sessionMode = component "Session mode button, modal and stop action.";
      prStatus = component "PR status source and row.";
      sessionName = component "Session name (shown from Claude Code's session registry; off shows the session id).";
      links = component "Transcript link render hook.";
      modelEffort = component "Model and effort button and modal.";
      focus = component "Focus pane button, pane and contribution storage.";
    };

    links = lib.mkOption {
      type = lib.types.listOf (
        lib.types.submodule {
          options = {
            name = lib.mkOption {
              type = lib.types.str;
              description = "Link name.";
            };
            kind = lib.mkOption {
              type = lib.types.nullOr (
                lib.types.enum [
                  "regex"
                  "github-pr"
                  "jira"
                  "file-ref"
                ]
              );
              default = null;
              description = "Rule kind. Null (the default) is a regex rule.";
            };
            pattern = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = "Pattern of the text to link (regex rules).";
            };
            url = lib.mkOption {
              type = lib.types.nullOr lib.types.str;
              default = null;
              description = "URL template the match links to (regex rules).";
            };
            settings = lib.mkOption {
              type = lib.types.attrsOf lib.types.anything;
              default = { };
              description = ''
                Kind-specific options, rendered as the rule's `options`: `github-pr` takes
                `sessionRepoFallback`, `jira` takes `baseUrl` and `keys`, `file-ref` takes
                `localTemplate`. Left out of the rendered rule when empty.
              '';
            };
          };
        }
      );
      default = [ ];
      description = "Transcript link rules. The shipped list is empty.";
    };

    github = {
      host = lib.mkOption {
        type = lib.types.str;
        default = "github.com";
        description = "GitHub host the github-pr and file-ref links point at.";
      };
      repos = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Known repositories, each written owner/repo.";
      };
    };

    metrics.endpoint = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Endpoint the metrics exporter posts to. Null leaves the exporter a no-op.";
    };

    alerts.thresholds = {
      contextWarn = lib.mkOption {
        type = lib.types.number;
        default = 60;
        description = "Context usage percent at which the context warning shows.";
      };
      contextAlert = lib.mkOption {
        type = lib.types.number;
        default = 80;
        description = "Context usage percent at which the context alert shows.";
      };
      limit = lib.mkOption {
        type = lib.types.number;
        default = 80;
        description = "Usage-limit percent at which the limit alert shows.";
      };
    };
  };

  # Always rendered, so enable = false reaches the mod (absent, it would default to on).
  config.home.sessionVariables.PG_CLAUDE_DISPLAY_OPTIONS = builtins.toJSON rendered;
}

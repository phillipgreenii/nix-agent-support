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
# Defaults match today's shell line: every component on.
let
  cfg = config.phillipgreenii.programs.pgd;
  component =
    description:
    lib.mkOption {
      type = lib.types.bool;
      default = true;
      inherit description;
    };
  rendered = {
    inherit (cfg) enable components links;
    alerts = { inherit (cfg.alerts) thresholds; };
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
      beads = component "Beads contributions: dolt alert and Focus rows.";
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
            pattern = lib.mkOption {
              type = lib.types.str;
              description = "Pattern of the text to link.";
            };
            url = lib.mkOption {
              type = lib.types.str;
              description = "URL template the match links to.";
            };
          };
        }
      );
      default = [ ];
      description = "Transcript link rules. The shipped list is empty.";
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

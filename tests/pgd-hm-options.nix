# Eval-level test of home/programs/pgd: the module is evaluated against stubbed
# home-manager options (home.sessionVariables) and the rendered plugin options are inspected.
# Consumed by checks.<system>.test-pgd-hm-options. All values are EXAMPLE values.
{ lib }:
let
  evaluate =
    cfg:
    lib.evalModules {
      modules = [
        ../home/programs/pgd/default.nix
        (
          { lib, ... }:
          {
            # stub of the beads switch that components.beads defaults from
            options.phillipgreenii.programs.beads.enable = lib.mkEnableOption "beads";
            config.phillipgreenii.programs.beads.enable = true;
            options.home.sessionVariables = lib.mkOption {
              type = lib.types.attrsOf lib.types.str;
              default = { };
            };
          }
        )
        { phillipgreenii.programs.pgd = cfg; }
      ];
    };

  opts = c: c.config.phillipgreenii.programs.pgd;
  rendered = c: builtins.fromJSON c.config.phillipgreenii.programs.pgd.optionsJson;

  defaults = evaluate { };
  custom = evaluate {
    enable = false;
    components.beads = false;
    links = [
      {
        name = "example";
        pattern = "EX-[0-9]+";
        url = "https://example.invalid/EX";
      }
    ];
    alerts.thresholds.contextWarn = 50;
  };

  componentNames = [
    "beads"
    "assume"
    "sessionMode"
    "prStatus"
    "sessionName"
    "links"
    "modelEffort"
    "focus"
  ];

  # New-shape rules: a kind, settings rendered as options, and a top-level github object.
  newShape = evaluate {
    github = {
      host = "git.example.invalid";
      repos = [ "own/repo" ];
    };
    links = [
      {
        name = "old";
        pattern = "EX-[0-9]+";
        url = "https://example.invalid/EX";
      }
      {
        name = "tk";
        kind = "jira";
        settings = {
          baseUrl = "https://tracker.invalid/browse";
          keys = [ "AB" ];
        };
      }
      {
        name = "pr";
        kind = "github-pr";
      }
      {
        name = "rx";
        kind = "regex";
        pattern = "X";
        url = "https://example.invalid/{0}";
        settings = { };
      }
    ];
  };

  cases = {
    # the exact JSON text an old kind-less entry rendered to before this change
    oldEntryRendersByteIdentically =
      builtins.toJSON (builtins.head (rendered custom).links)
      == ''{"name":"example","pattern":"EX-[0-9]+","url":"https://example.invalid/EX"}'';
    newShapeLinksRendered =
      (rendered newShape).links == [
        {
          name = "old";
          pattern = "EX-[0-9]+";
          url = "https://example.invalid/EX";
        }
        {
          name = "tk";
          kind = "jira";
          options = {
            baseUrl = "https://tracker.invalid/browse";
            keys = [ "AB" ];
          };
        }
        {
          name = "pr";
          kind = "github-pr";
        }
        {
          name = "rx";
          kind = "regex";
          pattern = "X";
          url = "https://example.invalid/{0}";
        }
      ];
    newShapeGithubRendered =
      (rendered newShape).github == {
        host = "git.example.invalid";
        repos = [ "own/repo" ];
      };
    kindSettingsDefaultNull =
      let
        l = builtins.head (opts custom).links;
      in
      l.kind == null && l.settings == { };
    everyComponentDeclaredAndOn = lib.all (n: (opts defaults).components.${n} == true) componentNames;
    onlyTheNamedComponents =
      lib.sort lib.lessThan (builtins.attrNames (opts defaults).components)
      == lib.sort lib.lessThan componentNames;
    enableDefaultsOn = (opts defaults).enable == true;
    linksDefaultEmpty = (opts defaults).links == [ ];
    thresholdDefaults =
      (opts defaults).alerts.thresholds == {
        contextWarn = 60;
        contextAlert = 80;
        limit = 80;
      };
    renderedDefaults =
      rendered defaults == {
        enable = true;
        components = lib.genAttrs componentNames (_: true);
        links = [ ];
        github = {
          host = "github.com";
          repos = [ ];
        };
        alerts.thresholds = {
          contextWarn = 60;
          contextAlert = 80;
          limit = 80;
        };
        metrics = { };
      };
    metricsEndpointDefaultsNull = (opts defaults).metrics.endpoint == null;
    metricsEndpointRendered =
      (rendered (evaluate {
        metrics.endpoint = "http://127.0.0.1:4318";
      })).metrics.endpoint == "http://127.0.0.1:4318";
    customRendered =
      let
        r = rendered custom;
      in
      r.enable == false
      && r.components.beads == false
      && r.components.assume == true
      &&
        r.links == [
          {
            name = "example";
            pattern = "EX-[0-9]+";
            url = "https://example.invalid/EX";
          }
        ]
      && r.alerts.thresholds.contextWarn == 50
      && r.alerts.thresholds.contextAlert == 80;
  };
  # A rule set whose patterns hold every character that is special to a shell inside or outside
  # double quotes: parentheses, a pipe, a hash after a space, a dollar sign, a backtick, a backslash
  # and a double quote. The flake check writes the escaped session variable the way home-manager does
  # (`export NAME="VALUE"`), sources it with a real bash, and compares the result with `json`.
  shellCase =
    let
      c = evaluate {
        links = [
          {
            name = "hostile";
            pattern = "(?<=\\b(?:PR|pull request) #?)[0-9]+\\b|$HOME|`id`|a\"b";
            url = "https://example.invalid/{0}";
          }
        ];
      };
    in
    {
      json = c.config.phillipgreenii.programs.pgd.optionsJson;
      escaped = c.config.home.sessionVariables.PG_CLAUDE_DISPLAY_OPTIONS;
    };
in
{
  inherit cases shellCase;
  failures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) cases);
}

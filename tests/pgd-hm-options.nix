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
  rendered = c: builtins.fromJSON c.config.home.sessionVariables.PG_CLAUDE_DISPLAY_OPTIONS;

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

  cases = {
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
in
{
  inherit cases;
  failures = builtins.attrNames (lib.filterAttrs (_: ok: !ok) cases);
}

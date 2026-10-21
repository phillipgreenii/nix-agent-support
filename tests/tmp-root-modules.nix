# Eval-level test of the two temp-root home-manager modules (bead pg2-2qirl,
# packet A of the build_tmp design): home/programs/build-tmp and
# home/programs/claude-code-tmp, both built on lib/tmp-root.nix. Consumed by
# checks.<system>.test-tmp-root-modules in flake.nix, which also runs the bats
# suites over the rendered artifacts (`artifacts` below).
#
# The modules are evaluated with STUBS for what this flake does not declare
# (home-manager's programs.zsh / programs.bash / home.activation and
# lib.hm.dag; the pg-disk-reclaimer enable flag). The REAL
# claude-extended-tool-approver module is imported where CETA is present, so the
# option names, the `extraTempRoots` assertion (which every contributed root must
# pass) and the list-merge with a consumer value are exercised for real.
#
# Returns { failures; artifacts; }. `failures` is the list of failed case names
# (empty when everything holds). `artifacts` are the rendered shell snippets and
# activation scripts, with the root spelled /ROOT_PLACEHOLDER/<basename>, for the
# bats suites to point at a real fixture directory.
{ lib, pkgs }:
let
  # home-manager's lib.hm.dag.entryAfter, reduced to a record so the dependency
  # list and the text can both be asserted on.
  hmLib = lib // {
    hm = (lib.hm or { }) // {
      dag = (lib.hm.dag or { }) // {
        entryAfter = deps: text: { inherit deps text; };
      };
    };
  };

  stubOptions =
    { lib, ... }:
    {
      options = {
        programs = {
          zsh = {
            enable = lib.mkOption {
              type = lib.types.bool;
              default = false;
            };
            envExtra = lib.mkOption {
              type = lib.types.lines;
              default = "";
            };
          };
          bash = {
            enable = lib.mkOption {
              type = lib.types.bool;
              default = false;
            };
            profileExtra = lib.mkOption {
              type = lib.types.lines;
              default = "";
            };
            bashrcExtra = lib.mkOption {
              type = lib.types.lines;
              default = "";
            };
          };
        };
        home.activation = lib.mkOption {
          type = lib.types.attrsOf lib.types.anything;
          default = { };
        };
        assertions = lib.mkOption {
          type = lib.types.listOf lib.types.unspecified;
          default = [ ];
        };
      };
    };

  reclaimerStub =
    { lib, ... }:
    {
      options.phillipgreenii.programs.pg-disk-reclaimer = {
        enable = lib.mkEnableOption "pg-disk-reclaimer (stub)";
        # Packet B: the modules contribute a registry item; the real option's schema
        # is exercised by tests/tmp-root-registry.nix.
        registryEntries = lib.mkOption {
          type = lib.types.listOf lib.types.attrs;
          default = [ ];
        };
      };
    };

  # Stubs the real CETA module needs (the same set test-ceta-extra-temp-roots uses).
  cetaStubPkg = pkgs.runCommand "ceta-stub-0" { } ''
    mkdir -p $out/bin
    printf '#!${pkgs.runtimeShell}\n' > $out/bin/claude-extended-tool-approver
    chmod +x $out/bin/claude-extended-tool-approver
  '';
  cetaStubs =
    { lib, ... }:
    {
      options = {
        phillipgreenii.programs.claude-code.enable = lib.mkEnableOption "claude (stub)";
        phillipgreenii.programs.claude-code.knownAbsentRoots = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
        };
        home.homeDirectory = lib.mkOption {
          type = lib.types.str;
          default = "/home/test";
        };
        home.packages = lib.mkOption {
          type = lib.types.listOf lib.types.package;
          default = [ ];
        };
        warnings = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = [ ];
        };
      };
    };
  cetaEnabled = {
    phillipgreenii.programs.claude-code.enable = true;
    phillipgreenii.programs.claude-extended-tool-approver = {
      enable = true;
      package = cetaStubPkg;
    };
  };

  buildPath = "/ROOT_PLACEHOLDER/build_tmp";
  claudePath = "/ROOT_PLACEHOLDER/claude_code_tmp";

  evalWith =
    {
      ceta ? false,
      reclaimer ? true,
      cfg ? { },
    }:
    (lib.evalModules {
      specialArgs = {
        inherit pkgs;
        lib = hmLib;
      };
      modules = [
        ../home/programs/build-tmp/default.nix
        ../home/programs/claude-code-tmp/default.nix
        stubOptions
        cfg
      ]
      ++ lib.optionals ceta [
        ../home/programs/claude-extended-tool-approver/default.nix
        cetaStubs
        cetaEnabled
      ]
      ++ lib.optional reclaimer reclaimerStub
      ++ lib.optional reclaimer { phillipgreenii.programs.pg-disk-reclaimer.enable = true; };
    }).config;

  # Variant: the reclaimer module IS declared but not enabled.
  evalReclaimerOff =
    cfg:
    (lib.evalModules {
      specialArgs = {
        inherit pkgs;
        lib = hmLib;
      };
      modules = [
        ../home/programs/build-tmp/default.nix
        ../home/programs/claude-code-tmp/default.nix
        stubOptions
        reclaimerStub
        cfg
      ];
    }).config;

  shells = {
    programs.zsh.enable = true;
    programs.bash.enable = true;
  };
  buildOn = {
    phillipgreenii.programs.build-tmp = {
      enable = true;
      path = buildPath;
    };
  };
  claudeOn = {
    phillipgreenii.programs.claude-code-tmp = {
      enable = true;
      path = claudePath;
    };
  };
  both = {
    imports = [
      buildOn
      claudeOn
      shells
    ];
  };

  failedAssertions = c: lib.filter (a: !a.assertion) c.assertions;
  messages = c: map (a: a.message) (failedAssertions c);
  anyMessage = c: pred: lib.any pred (messages c);
  has = needle: hay: lib.hasInfix needle hay;

  ceta = c: c.phillipgreenii.programs.claude-extended-tool-approver;

  off = evalWith { cfg = shells; };
  buildOnlyCfg = {
    imports = [
      buildOn
      shells
    ];
  };
  claudeOnlyCfg = {
    imports = [
      claudeOn
      shells
    ];
  };
  buildOnlyEval = evalWith { cfg = buildOnlyCfg; };
  claudeOnlyEval = evalWith { cfg = claudeOnlyCfg; };
  bothEval = evalWith { cfg = both; };
  tmpdirEval = evalWith {
    cfg = {
      imports = [
        buildOn
        shells
        { phillipgreenii.programs.build-tmp.exportTmpdir = true; }
      ];
    };
  };
  noShellEval = evalWith { cfg = buildOn; };
  spaceEval = evalWith {
    cfg = {
      imports = [ shells ];
      phillipgreenii.programs.build-tmp = {
        enable = true;
        path = "/ROOT_PLACEHOLDER/with space/build_tmp";
      };
    };
  };
  cetaBoth = evalWith {
    ceta = true;
    cfg = both;
  };
  cetaBuildOnly = evalWith {
    ceta = true;
    cfg = buildOnlyCfg;
  };
  cetaClaudeOnly = evalWith {
    ceta = true;
    cfg = claudeOnlyCfg;
  };
  cetaNeither = evalWith {
    ceta = true;
    cfg = shells;
  };
  cetaMerged = evalWith {
    ceta = true;
    cfg = {
      imports = [
        buildOn
        { phillipgreenii.programs.claude-extended-tool-approver.extraReadWriteRoots = [ "/consumer/rw" ]; }
      ];
    };
  };
  slashEval = evalWith {
    ceta = true;
    cfg.phillipgreenii.programs.build-tmp = {
      enable = true;
      path = "/a//vol/build_tmp/";
    };
  };
  wrongBase = evalWith {
    cfg.phillipgreenii.programs.build-tmp = {
      enable = true;
      path = "/ROOT_PLACEHOLDER/other";
    };
  };
  wrongBaseClaude = evalWith {
    cfg.phillipgreenii.programs.claude-code-tmp = {
      enable = true;
      path = buildPath;
    };
  };
  relativePath = evalWith {
    cfg.phillipgreenii.programs.build-tmp = {
      enable = true;
      path = "relative/build_tmp";
    };
  };
  dotSegment = evalWith {
    cfg.phillipgreenii.programs.build-tmp = {
      enable = true;
      path = "/a/../build_tmp";
    };
  };
  reclaimerOff = evalReclaimerOff buildOn;
  reclaimerAbsent = evalWith {
    reclaimer = false;
    cfg = claudeOn;
  };
  # CETA absent + claude-code-tmp enabled evaluates cleanly (no undeclared-option error).
  cetaAbsentClaude = evalWith { cfg = claudeOn; };
  evaluates = c: (builtins.tryEval (builtins.deepSeq (failedAssertions c) true)).success;
  levels = evalWith {
    cfg = {
      imports = [
        buildOn
        claudeOn
        {
          phillipgreenii.programs.build-tmp.level1Minutes = 30;
          phillipgreenii.programs.claude-code-tmp.level3Minutes = 7;
        }
      ];
    };
  };
  defaultLevels = bothEval.phillipgreenii.programs;

  zshOf = c: c.programs.zsh.envExtra;
  actOf = c: n: c.home.activation.${n};

  cases = [
    {
      name = "both modules are off by default: no activation, no shell text, no assertions";
      ok =
        off.home.activation == { }
        && zshOf off == ""
        && off.programs.bash.profileExtra == ""
        && off.programs.bash.bashrcExtra == ""
        && off.assertions == [ ]
        && !off.phillipgreenii.programs.build-tmp.enable
        && !off.phillipgreenii.programs.claude-code-tmp.enable;
    }
    {
      name = "build-tmp enabled: activation entry runs after writeBoundary, creates 0700 dir and marker";
      ok =
        (actOf buildOnlyEval "buildTmpRoot").deps == [ "writeBoundary" ]
        && has ".build-tmp-root" (actOf buildOnlyEval "buildTmpRoot").text
        && has "chmod 0700" (actOf buildOnlyEval "buildTmpRoot").text
        && has "$DRY_RUN_CMD mkdir -p" (actOf buildOnlyEval "buildTmpRoot").text
        && !(buildOnlyEval.home.activation ? claudeCodeTmpRoot)
        && failedAssertions buildOnlyEval == [ ];
    }
    {
      name = "build-tmp snippet exports GOTMPDIR and PG_TEST_RUNNER_TMP_ROOT, no TMPDIR by default";
      ok =
        has "export GOTMPDIR=" (zshOf buildOnlyEval)
        && has "export PG_TEST_RUNNER_TMP_ROOT=" (zshOf buildOnlyEval)
        && !(has "export TMPDIR=" (zshOf buildOnlyEval))
        && !(has "CLAUDE_CODE_TMPDIR" (zshOf buildOnlyEval));
    }
    {
      name = "build-tmp exportTmpdir = true renders the TMPDIR line";
      ok = has "export TMPDIR=" (zshOf tmpdirEval) && has "export GOTMPDIR=" (zshOf tmpdirEval);
    }
    {
      name = "the bash profile and rc extras carry the same text as zsh envExtra";
      ok =
        buildOnlyEval.programs.bash.profileExtra == zshOf buildOnlyEval
        && buildOnlyEval.programs.bash.bashrcExtra == zshOf buildOnlyEval
        && zshOf buildOnlyEval != "";
    }
    {
      name = "with the shell flags off the activation still renders but no shell text does";
      ok =
        noShellEval.home.activation ? buildTmpRoot
        && zshOf noShellEval == ""
        && noShellEval.programs.bash.profileExtra == ""
        && noShellEval.programs.bash.bashrcExtra == "";
    }
    {
      name = "claude-code-tmp alone: its own activation entry, CLAUDE_CODE_TMPDIR only";
      ok =
        has ".claude-code-tmp-root" (actOf claudeOnlyEval "claudeCodeTmpRoot").text
        && has "export CLAUDE_CODE_TMPDIR=" (zshOf claudeOnlyEval)
        && !(has "GOTMPDIR" (zshOf claudeOnlyEval))
        && !(claudeOnlyEval.home.activation ? buildTmpRoot)
        && failedAssertions claudeOnlyEval == [ ];
    }
    {
      name = "both enabled: two activation entries and both snippets";
      ok =
        bothEval.home.activation ? buildTmpRoot
        && bothEval.home.activation ? claudeCodeTmpRoot
        && has "export GOTMPDIR=" (zshOf bothEval)
        && has "export CLAUDE_CODE_TMPDIR=" (zshOf bothEval)
        && failedAssertions bothEval == [ ];
    }
    {
      name = "no rendered text anywhere mentions CETA_EXTRA_TEMP_ROOTS";
      ok =
        !(has "CETA_EXTRA_TEMP_ROOTS" (
          builtins.toJSON bothEval.home.activation
          + zshOf bothEval
          + bothEval.programs.bash.profileExtra
          + bothEval.programs.bash.bashrcExtra
        ));
    }
    {
      name = "a basename other than build_tmp / claude_code_tmp fails the assertion";
      ok =
        lib.length (failedAssertions wrongBase) == 1
        && anyMessage wrongBase (m: has "must end in \"build_tmp\"" m)
        && lib.length (failedAssertions wrongBaseClaude) == 1
        && anyMessage wrongBaseClaude (m: has "must end in \"claude_code_tmp\"" m);
    }
    {
      name = "a relative path and a '..' segment each fail an assertion";
      ok =
        anyMessage relativePath (m: has "absolute" m) && anyMessage dotSegment (m: has "'..' segment" m);
    }
    {
      name = "a root enabled while pg-disk-reclaimer is disabled fails, naming pg-disk-reclaimer.enable";
      ok =
        lib.length (failedAssertions reclaimerOff) == 1
        && anyMessage reclaimerOff (m: has "phillipgreenii.programs.pg-disk-reclaimer.enable" m);
    }
    {
      name = "a root enabled while the pg-disk-reclaimer module is not imported fails (and does not touch the option)";
      ok =
        evaluates reclaimerAbsent
        && lib.length (failedAssertions reclaimerAbsent) == 1
        && anyMessage reclaimerAbsent (m: has "pg-disk-reclaimer.enable" m);
    }
    {
      name = "disabled modules add no assertion even when pg-disk-reclaimer is disabled";
      ok = (evalReclaimerOff shells).assertions == [ ];
    }
    {
      name = "CETA present, both enabled: both roots are temp AND read-write, CETA's own assertions pass";
      ok =
        lib.sort lib.lessThan (ceta cetaBoth).extraTempRoots == [
          buildPath
          claudePath
        ]
        &&
          lib.sort lib.lessThan (ceta cetaBoth).extraReadWriteRoots == [
            buildPath
            claudePath
          ]
        && failedAssertions cetaBoth == [ ];
    }
    {
      name = "CETA present, only build-tmp: only its root is contributed, to both facets";
      ok =
        (ceta cetaBuildOnly).extraTempRoots == [ buildPath ]
        && (ceta cetaBuildOnly).extraReadWriteRoots == [ buildPath ]
        && (ceta cetaClaudeOnly).extraTempRoots == [ claudePath ]
        && (ceta cetaClaudeOnly).extraReadWriteRoots == [ claudePath ];
    }
    {
      name = "CETA present, no root enabled: nothing contributed";
      ok =
        (ceta cetaNeither).extraTempRoots == [ ]
        && (ceta cetaNeither).extraReadWriteRoots == [ ]
        && failedAssertions cetaNeither == [ ];
    }
    {
      name = "CETA contributions list-merge with a consumer's own extraReadWriteRoots";
      ok =
        lib.sort lib.lessThan (ceta cetaMerged).extraReadWriteRoots == [
          buildPath
          "/consumer/rw"
        ]
        && (ceta cetaMerged).extraTempRoots == [ buildPath ];
    }
    {
      name = "repeated and trailing slashes are collapsed before the root reaches CETA and the scripts";
      ok =
        (ceta slashEval).extraTempRoots == [ "/a/vol/build_tmp" ]
        && (ceta slashEval).extraReadWriteRoots == [ "/a/vol/build_tmp" ]
        && has "/a/vol/build_tmp" (actOf slashEval "buildTmpRoot").text
        && failedAssertions slashEval == [ ];
    }
    {
      name = "claude-code-tmp enabled while the CETA module is absent evaluates cleanly";
      ok = evaluates cetaAbsentClaude && failedAssertions cetaAbsentClaude == [ ];
    }
    {
      name = "idle-time options: defaults and overrides";
      ok =
        defaultLevels.build-tmp.level1Minutes == 1440
        && defaultLevels.build-tmp.level3Minutes == 60
        && defaultLevels.claude-code-tmp.level1Minutes == 20160
        && defaultLevels.claude-code-tmp.level3Minutes == 2880
        && levels.phillipgreenii.programs.build-tmp.level1Minutes == 30
        && levels.phillipgreenii.programs.build-tmp.level3Minutes == 60
        && levels.phillipgreenii.programs.claude-code-tmp.level1Minutes == 20160
        && levels.phillipgreenii.programs.claude-code-tmp.level3Minutes == 7;
    }
  ];

  render = file: text: pkgs.writeText file text;
  artifactsFor = c: {
    zsh = zshOf c;
    bash = c.programs.bash.profileExtra;
  };
in
{
  failures = map (c: c.name) (lib.filter (c: !c.ok) cases);
  caseCount = lib.length cases;
  artifacts = {
    buildSnippet = render "build-tmp-env.sh" (artifactsFor buildOnlyEval).zsh;
    buildSnippetTmpdir = render "build-tmp-env-tmpdir.sh" (artifactsFor tmpdirEval).zsh;
    buildSnippetSpace = render "build-tmp-env-space.sh" (artifactsFor spaceEval).zsh;
    claudeSnippet = render "claude-code-tmp-env.sh" (artifactsFor claudeOnlyEval).zsh;
    buildActivation = render "build-tmp-activation.sh" (actOf buildOnlyEval "buildTmpRoot").text;
    claudeActivation = render "claude-code-tmp-activation.sh" (actOf claudeOnlyEval "claudeCodeTmpRoot")
    .text;
  };
}

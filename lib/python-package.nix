# Helper for creating Python packages with dependency resolution and standard tooling
# This reduces boilerplate across Python-based packages
{
  pkgs,
  lib,
}:
{
  # Create a Python package with automatic dependency resolution from pyproject.toml
  # Usage: mkPythonPackage {
  #   name = "gh-prreview";
  #   gitHash = "abc1234";
  #   src = ./.;
  #   customDeps = {
  #     "eventsourcing" = eventsourcingPackage;
  #   };
  #   pypiToNixNameMappings = {
  #     "GitPython" = "gitpython";
  #   };
  #   runtimeDeps = [ pkgs.gh pkgs.git ];
  #   versionPlaceholder = "2.0.0";  # Version string in pyproject.toml to replace
  #   versionInitFile = "src/gh_prreview/__init__.py";  # Optional __init__.py with __version__
  #   hasCompletions = true;
  #   hasTldr = true;
  # }
  mkPythonPackage =
    {
      name,
      gitHash,
      src,
      customDeps ? { },
      pypiToNixNameMappings ? { },
      runtimeDeps ? [ ],
      versionPlaceholder ? "0.0.0",
      versionInitFile ? null,
      hasCompletions ? true,
      hasTldr ? true,
      extraNativeBuildInputs ? [ ],
      extraPostInstall ? "",
    }:
    let
      python = pkgs.python3;

      # Read pyproject.toml at eval-time
      pyprojectData = builtins.fromTOML (builtins.readFile "${src}/pyproject.toml");

      # Extract dependency specifications from pyproject.toml
      depSpecs = pyprojectData.project.dependencies or [ ];

      # Parse each dependency: "package>=1.2.3" -> "package"
      parseDep =
        depStr:
        let
          matched = builtins.match "([a-zA-Z0-9_-]+).*" depStr;
        in
        if matched != null then builtins.head matched else depStr;

      # Get just the package names
      depNames = map parseDep depSpecs;

      # Map PyPI names to Nix package names
      pypiToNixName = pypiName: pypiToNixNameMappings.${pypiName} or (lib.strings.toLower pypiName);

      # Try to resolve each package
      resolveDep =
        pypiName:
        let
          nixName = pypiToNixName pypiName;
        in
        # First check if we have a custom package
        if builtins.hasAttr pypiName customDeps then
          customDeps.${pypiName}
        # Then check if it's in nixpkgs
        else if builtins.hasAttr nixName python.pkgs then
          python.pkgs.${nixName}
        # Otherwise skip it with a warning
        else
          builtins.trace "Warning: Python package '${pypiName}' (as '${nixName}') not found in nixpkgs, skipping" null;

      # Generate propagatedBuildInputs, filtering out nulls (missing packages)
      propagatedBuildInputs = builtins.filter (x: x != null) (map resolveDep depNames);

    in
    python.pkgs.buildPythonApplication rec {
      pname = pyprojectData.project.name;
      version = "0.0.0"; # Placeholder; actual version computed at build time
      format = "pyproject";

      inherit src;

      # Build dependencies
      nativeBuildInputs =
        with python.pkgs;
        [
          setuptools
          wheel
          pkgs.help2man
          pkgs.libfaketime
        ]
        ++ extraNativeBuildInputs;

      # Runtime dependencies - auto-generated from pyproject.toml
      inherit propagatedBuildInputs;

      # Make external tools available at runtime
      makeWrapperArgs = lib.optionals (runtimeDeps != [ ]) [
        "--prefix"
        "PATH"
        ":"
        "${lib.makeBinPath runtimeDeps}"
      ];

      # Don't run tests during build
      doCheck = false;

      # Replace DEV version with actual build version
      preBuild = ''
        # Compute build version (PEP 440 compliant: use + for local version)
        SECONDS_TODAY=$(( $(date +%s) % 86400 ))
        BUILD_VERSION=$(printf "%s.%05d+%s" "$(date +%y.%m.%d)" "$SECONDS_TODAY" "${gitHash}")

        # Replace placeholder with actual version in pyproject.toml
        substituteInPlace pyproject.toml \
          --replace-fail 'version = "${versionPlaceholder}"' "version = \"$BUILD_VERSION\""

        ${lib.optionalString (versionInitFile != null) ''
          # Replace version in __init__.py if specified
          substituteInPlace ${versionInitFile} \
            --replace-fail '__version__ = "${versionPlaceholder}"' "__version__ = \"$BUILD_VERSION\""
        ''}
      '';

      # Disable runtime dependency check since we handle deps manually
      dontCheckRuntimeDeps = true;

      # Install completion scripts and generate man page
      postInstall = ''
        mkdir -p $out/share/man/man1
        ${lib.optionalString hasCompletions ''
          mkdir -p $out/share/bash-completion/completions
          mkdir -p $out/share/zsh/site-functions
        ''}
        ${lib.optionalString hasTldr ''
          mkdir -p $out/share/tldr/pages.common
        ''}

        ${lib.optionalString hasCompletions ''
          # Install completion scripts if they exist
          if [ -f completions/${name}.bash ]; then
            cp completions/${name}.bash $out/share/bash-completion/completions/${name}
          fi
          if [ -f completions/_${name} ]; then
            cp completions/_${name} $out/share/zsh/site-functions/_${name}
          fi
        ''}

        # Generate man page with current date
        export SOURCE_DATE_EPOCH=$(date +%s)
        ${pkgs.help2man}/bin/help2man --no-info \
          --name="${pyprojectData.project.description}" \
          $out/bin/${name} > $out/share/man/man1/${name}.1

        ${lib.optionalString hasTldr ''
          # Install tldr page if it exists
          if [ -f ${name}.md ]; then
            cp ${name}.md $out/share/tldr/pages.common/
          fi
        ''}

        ${extraPostInstall}
      '';

      meta = with lib; {
        inherit (pyprojectData.project) description;
        platforms = platforms.darwin ++ platforms.linux;
        mainProgram = name;
      };
    };
}

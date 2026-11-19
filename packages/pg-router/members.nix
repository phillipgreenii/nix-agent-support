# The router module's member boundary, as nix values. The data lives in
# members.json, which the Go boundary test (boundary_test.go) reads too, so the
# nix filesets and the tested import rule cannot drift apart.
#
# Folded members keep their own packages/<name> directory holding default.nix
# only; their sources live under this module (cmd/<binary>, internal/<member>).
# A member binary's fileset is exactly its cmd directories, its internal
# subtree, the shared set, and the module files. The router's own fileset is
# everything except the member cmd directories and internal subtrees, so a
# member-only edit does not change the router's --version.
{ lib }:
let
  data = builtins.fromJSON (builtins.readFile ./members.json);
  here = ./.;
  # A path a member lists may not exist yet (the heartbeat has no internal
  # subtree); the fileset functions reject a missing path.
  existing = lib.filter builtins.pathExists;
  # Directories a member owns inside a shared directory (the handlers' own
  # conformance tests sit under conformance/).
  memberExtra = m: m.extra or [ ];
  memberPaths =
    m:
    existing (
      (map (c: here + "/cmd/${c}") m.cmd)
      ++ (map (i: here + "/internal/${i}") m.internal)
      ++ (map (e: here + "/${e}") (memberExtra m))
    );
  allMemberPaths = lib.concatMap memberPaths (lib.attrValues data.members);
  allExtraPaths = map (e: here + "/${e}") (lib.concatMap memberExtra (lib.attrValues data.members));
  # The shared directories without any member's own subdirectory inside them.
  sharedFileset = lib.fileset.difference (lib.fileset.unions (
    existing (map (p: here + "/${p}") data.shared)
  )) (lib.fileset.unions allExtraPaths);
  moduleFiles = [
    (here + "/go.mod")
    (here + "/go.sum")
    (here + "/gomod2nix.toml")
  ];
in
rec {
  inherit data;

  # Directory under packages/ that roots every fileset below.
  packagesRoot = here + "/..";

  # `lib.fileset` value for member `name`, to be used with root = packages/.
  memberFileset =
    name:
    lib.fileset.unions (
      moduleFiles
      ++ memberPaths data.members.${name}
      ++ [ sharedFileset ]
      ++ [ (here + "/../claude-transcript") ]
    );

  # `lib.fileset` value for the router itself (the docs tree and every member
  # tree removed).
  routerFileset = lib.fileset.difference (lib.fileset.difference here (here + "/docs")) (
    lib.fileset.unions allMemberPaths
  );

  # Shell fragment: `go list` over the router's own subtree (every package
  # except the members').
  routerTestPackages = ''
    $(go list ./... | grep -Ev '/(${
      lib.concatStringsSep "|" (
        lib.concatMap (
          m: map (c: "cmd/${c}") m.cmd ++ map (i: "internal/${i}") m.internal ++ memberExtra m
        ) (lib.attrValues data.members)
      )
    })(/|$)')
  '';

  # Package patterns `go test` takes for member `name`.
  memberTestPackages =
    name:
    lib.concatStringsSep " " (
      map (c: "./cmd/${c}/...") data.members.${name}.cmd
      ++ map (i: "./internal/${i}/...") (
        lib.filter (i: builtins.pathExists (here + "/internal/${i}")) data.members.${name}.internal
      )
      ++ map (e: "./${e}/...") (memberExtra data.members.${name})
    );
}

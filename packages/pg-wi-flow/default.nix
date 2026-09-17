{
  pkgs,
  bashBuilders,
}:
let
  # actor (lib/actor.bash): pg-wi-flow CLI actor composition (bead tc-q25wo
  # item 2). No script wraps it yet -- the actual claim/write verbs are later
  # rows in the tc-9ddu3 Phase-1 build plan; this bead only scaffolds the
  # library those verbs will source.
  actor = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
  };

  # pg-wi-flow-identity: the ceta input processor (bead tc-q25wo item 1).
  pg-wi-flow-identity = pkgs.callPackage ./pg-wi-flow-identity {
    inherit (bashBuilders) mkBashScript;
  };
in
{
  inherit actor pg-wi-flow-identity;
  inherit (pg-wi-flow-identity) packages tldr;
  checks = {
    test-pg-wi-flow-actor = actor.check;
    test-pg-wi-flow-identity = pg-wi-flow-identity.check;
  };
}

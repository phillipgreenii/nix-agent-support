{
  mkBashScript,
  pkgs,
}:
mkBashScript {
  name = "pg-disk-reclaimer";
  src = ./.;
  description = "Data-driven disk-space-reclaim CLI";
  # jq reads/validates the registry JSON file. Nothing in this scaffold
  # (bead pg2-txxyj.1) actually parses the registry yet -- registry loading
  # lands with pg2-txxyj.2 -- but the registry format is fixed JSON (per the
  # pg2-txxyj epic), so jq is declared up front rather than added piecemeal.
  runtimeDeps = [
    pkgs.jq
  ];
}

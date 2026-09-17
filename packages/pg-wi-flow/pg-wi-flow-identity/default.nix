{
  mkBashScript,
}:
mkBashScript {
  name = "pg-wi-flow-identity";
  src = ./.;
  description = "ceta input processor: stamps a per-agent PG_WI_FLOW_IDENT onto pg-wi-flow invocations (bead tc-q25wo item 1)";
  # No runtimeDeps: the processor is pure bash (printf/parameter expansion,
  # no external commands), matching internal/inputproc's 3s-per-processor
  # budget with margin to spare.
}

{
  mkBashLibrary,
}:
mkBashLibrary {
  name = "actor";
  src = ./.;
  description = "pg-wi-flow CLI actor composition: PG_WI_FLOW_IDENT + stage, or an explicit --actor override (bead tc-q25wo item 2)";
}

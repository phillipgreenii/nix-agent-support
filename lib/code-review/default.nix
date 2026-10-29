# Code-review principles: the ONE agent-agnostic, repo-agnostic, tool-agnostic definition of
# how to review code, plus a build-time composer that assembles it into each consumer's text.
#
# Pure evaluation, no derivation: consumers take it as `inputs.<agent-support>.lib.codeReview`
# (see docs/guides/code-review-principles.md). Nothing here reads a path at runtime; the fragment
# text is embedded at evaluation time, so a consumer never hardcodes a repo or a path.
#
#   principles                  every fragment, in order, joined: the common text
#   fragments.<name>            one fragment's text (one concern per file)
#   fragmentNames               the ordered names; `fragmentsDir` is the source directory
#   compose { fragments, addendum, forbid }
#                               joins the chosen fragments (a list of names, default all) and
#                               a consumer's scenario-specific addendum into one string with
#                               no trailing newline. It THROWS (so evaluation fails) when any
#                               fragment or the addendum contains a substring of `forbid`
#                               (default: the Go-template opener, which would break a prompt
#                               that is itself a Go template).
#
# The fragments MUST stay agnostic: no repo, tracker, host, vendor or organisation names.
# This repo carries no word list for that (see its CLAUDE.md); each consumer checks the
# composed text against its own vocabulary. Scenario detail belongs in an addendum.
#   offenders { fragments, addendum, forbid }
#                               the names of the pieces `compose` would reject.
{ lib }:
let
  fragmentsDir = ./fragments;

  # The order is the reading order of `principles`. A fragment file not listed here, or a
  # name listed with no file, fails tests/code-review.nix.
  fragmentNames = [
    "voice"
    "severity"
    "evidence"
    "scope"
    "intent-and-standards"
    "correctness-security"
    "tests"
    "rereview"
  ];

  fragments = lib.genAttrs fragmentNames (
    name: stripTrailingNewlines (builtins.readFile (fragmentsDir + "/${name}.md"))
  );

  defaultForbid = [ "{{" ];

  stripTrailingNewlines =
    s: if lib.hasSuffix "\n" s then stripTrailingNewlines (lib.removeSuffix "\n" s) else s;

  # The pieces a compose call joins, each with a name for error messages.
  partsOf =
    { fragments, addendum }:
    let
      pieces = map (n: {
        name = "fragment '${n}'";
        text =
          fragments'.${n}
            or (throw "codeReview.compose: unknown fragment '${n}' (known: ${lib.concatStringsSep ", " fragmentNames})");
      }) fragments;
      addendumText = stripTrailingNewlines addendum;
    in
    pieces
    ++ lib.optional (addendumText != "") {
      name = "the addendum";
      text = addendumText;
    };

  # Names of the pieces that contain a forbidden substring (exposed for the tests).
  offenders =
    {
      fragments ? fragmentNames,
      addendum ? "",
      forbid ? defaultForbid,
    }:
    map (p: p.name) (
      lib.filter (p: lib.any (f: lib.hasInfix f p.text) forbid) (partsOf {
        inherit fragments addendum;
      })
    );

  compose =
    {
      fragments ? fragmentNames,
      addendum ? "",
      forbid ? defaultForbid,
    }:
    let
      bad = offenders { inherit fragments addendum forbid; };
    in
    if bad != [ ] then
      throw "codeReview.compose: ${lib.head bad} contains a forbidden substring (${lib.concatStringsSep ", " forbid})"
    else
      lib.concatStringsSep "\n\n" (
        map (p: p.text) (partsOf {
          inherit fragments addendum;
        })
      );

  # `compose`'s own argument is named `fragments`, so the attrset is aliased for its body.
  fragments' = fragments;
in
{
  inherit
    fragments
    fragmentNames
    fragmentsDir
    compose
    offenders
    ;
  principles = compose { };
}

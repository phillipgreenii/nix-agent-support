# Eval-level cases for lib/code-review (the common code-review principles and their
# composer). Returns { failures; caseCount; }: a non-empty `failures` fails
# checks.<system>.test-code-review (flake.nix), which also scans the fragment files for
# non-agnostic words.
{ lib }:
let
  cr = import ../lib/code-review { inherit lib; };

  # A case is { name; ok; }. `throws` is true when forcing the expression fails evaluation.
  throws = expr: !(builtins.tryEval (builtins.deepSeq expr expr)).success;

  onDisk = lib.sort lib.lessThan (
    map (f: lib.removeSuffix ".md" f) (
      lib.attrNames (
        lib.filterAttrs (n: t: t == "regular" && lib.hasSuffix ".md" n) (builtins.readDir cr.fragmentsDir)
      )
    )
  );

  # Whole-word RFC 2119 keyword present (split on non-letters; a regex over the multi-line text
  # does not work in nix, whose `[\s\S]` matches nothing).
  hasKeyword =
    text:
    lib.any (
      w:
      lib.elem w [
        "MUST"
        "SHOULD"
        "MAY"
      ]
    ) (lib.filter lib.isString (builtins.split "[^A-Za-z]+" text));

  sample = "## Scenario\n\nSample addendum.\n";

  cases = [
    {
      name = "every fragment file is listed and every listed name has a file";
      ok = onDisk == lib.sort lib.lessThan cr.fragmentNames;
    }
    {
      name = "fragment names are unique";
      ok = lib.unique cr.fragmentNames == cr.fragmentNames;
    }
    {
      name = "every fragment is non-empty, opens with a level-2 heading, and has no trailing newline";
      ok = lib.all (
        n:
        cr.fragments.${n} != ""
        && lib.hasPrefix "## " cr.fragments.${n}
        && !lib.hasSuffix "\n" cr.fragments.${n}
      ) cr.fragmentNames;
    }
    {
      name = "every fragment contains at least one MUST, SHOULD or MAY";
      ok = lib.all (n: hasKeyword cr.fragments.${n}) cr.fragmentNames;
    }
    {
      name = "the RFC 2119 matcher rejects a text with no keyword (negative control)";
      ok = !hasKeyword "a mustard maybe text" && hasKeyword "it MAY be, or must not";
    }
    {
      name = "no fragment contains the Go-template opener";
      ok = lib.all (n: !lib.hasInfix "{{" cr.fragments.${n}) cr.fragmentNames;
    }
    {
      name = "principles is every fragment in order, blank-line separated, no trailing newline";
      ok =
        cr.principles == lib.concatStringsSep "\n\n" (map (n: cr.fragments.${n}) cr.fragmentNames)
        && !lib.hasSuffix "\n" cr.principles;
    }
    {
      name = "compose with an addendum appends it after a blank line and trims its trailing newline";
      ok = cr.compose { addendum = sample; } == cr.principles + "\n\n## Scenario\n\nSample addendum.";
    }
    {
      name = "compose with an empty addendum equals principles";
      ok = cr.compose { addendum = ""; } == cr.principles;
    }
    {
      name = "compose with a subset keeps the order given";
      ok =
        cr.compose {
          fragments = [
            "severity"
            "voice"
          ];
        } == "${cr.fragments.severity}\n\n${cr.fragments.voice}";
    }
    {
      name = "compose rejects an unknown fragment name";
      ok = throws (
        cr.compose {
          fragments = [ "no-such-fragment" ];
        }
      );
    }
    {
      name = "compose rejects a Go-template opener in the addendum";
      ok = throws (cr.compose { addendum = "x {{ .Item }} y"; });
    }
    {
      name = "compose scans the fragments themselves against forbid (negative control)";
      ok = throws (cr.compose { forbid = [ "MUST" ]; });
    }
    {
      name = "compose with forbid = [] accepts a Go-template opener in the addendum";
      ok =
        (cr.compose {
          addendum = "{{ ok }}";
          forbid = [ ];
        }) != "";
    }
    {
      name = "compose strips ALL trailing newlines of the addendum";
      ok = cr.compose { addendum = "## S\n\nx\n\n\n"; } == cr.principles + "\n\n## S\n\nx";
    }
    {
      name = "an addendum of only newlines adds nothing";
      ok = cr.compose { addendum = "\n\n"; } == cr.principles;
    }
    {
      name = "offenders names the addendum when it holds a forbidden substring";
      ok = cr.offenders { addendum = "a {{ b"; } == [ "the addendum" ];
    }
    {
      name = "offenders names the fragment that holds a forbidden substring";
      ok =
        cr.offenders {
          fragments = [ "voice" ];
          forbid = [ "MUST" ];
        } == [ "fragment 'voice'" ];
    }
    {
      name = "the fragment order is pinned";
      ok =
        cr.fragmentNames == [
          "voice"
          "severity"
          "evidence"
          "scope"
          "intent-and-standards"
          "correctness-security"
          "tests"
          "rereview"
        ];
    }
  ];

  failedCases = lib.filter (c: !c.ok) cases;
in
{
  failures = map (c: c.name) failedCases;
  caseCount = lib.length cases;
}

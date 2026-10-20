{
  config,
  lib,
  pkgs,
  ...
}:

let
  cetaCfg = config.phillipgreenii.programs.claude-extended-tool-approver;
in
{
  options.phillipgreenii.programs.rtk = {
    enable = lib.mkEnableOption "rtk (Rust Token Killer) - LLM token optimizer";
  };

  config = lib.mkIf config.phillipgreenii.programs.rtk.enable {
    home.packages = [ pkgs.llm-agentsPkgs.rtk ];

    # Wire rtk into ceta's ordered inputProcessors list (ADR 0071 Phase D2;
    # docket tc-rjzd3, packet tc-rjzd3.14: "a bead to actually wire rtk into
    # ceta's inputProcessors chain"), but only when ceta is itself enabled --
    # an rtk-only machine has nowhere for the processor to run.
    #
    # "rtk rewrite" is rtk's own low-level command-mutation primitive -- the
    # same one its Hermes plugin adapter uses ("terminal command mutation via
    # `rtk rewrite`", per rtk's own README's Supported Agents table), NOT the
    # newer `rtk hook claude` native hook, which expects Claude Code's own
    # full PreToolUse JSON envelope on stdin -- a different calling
    # convention than the `<command> "<bash-command>"` contract
    # inputProcessors requires.
    #
    # Confirmed against rtk's vendored source (src/hooks/rewrite_cmd.rs, rtk
    # 0.49.0, pinned by the llm-agents.nix package this module already
    # installs; re-verified against the deployed rtk 1.0.1, where exit 3 is
    # "rewrite printed, an ask rule matched or allow rules do not cover every
    # command"): `rtk rewrite <cmd>` exits 0 with the rewritten command on
    # stdout when a rewrite is allowed (RewriteOutcome::Allow) and exit 1
    # with empty stdout when there is no RTK equivalent (Passthrough) --
    # exactly matching inputProcessors' own "exit 0 + non-empty stdout =
    # rewrite; exit 1+ or empty stdout = decline" contract (see that
    # option's doc comment in
    # home/programs/claude-extended-tool-approver/default.nix). rtk's Deny
    # (exit 2) and Ask (exit 3, rewritten command also on stdout) outcomes
    # both also degrade to a decline here -- internal/inputproc's
    # runOneProcessor treats any non-zero, non-1 exit as a decline too (the
    # failure is merely logged) -- which is the correct degradation:
    # inputProcessors has no permission-decision concept of its own, so
    # rtk's own deny/ask rules do not reach this seam. This matches the
    # design's own ruling that rtk is not a router delegate and gains no
    # decision-making power here; it remains a pure rewrite processor inside
    # ceta's already-decided internal composition layer.
    #
    # A bare "rtk" (not a nix store path) resolves on PATH the same way
    # ceta's own bare `claude-extended-tool-approver` hook command does,
    # since rtk is already installed via home.packages above whenever this
    # module is enabled. This is a plain (non-mkAfter) list contribution at
    # normal priority: pg-wi-flow's own identity-processor contribution
    # (home/programs/pg-wi-flow/default.nix) is deliberately `mkAfter`ed so
    # it always sorts after this one.
    #
    # DROP RULE (pg2-jwilh): ceta drops an rtk rewrite (emits no updatedInput,
    # so the original runs) when the payload cwd is inside a linked git
    # worktree and the rewrite is guard-hostile -- a non-git-named executable
    # with a git-named word among its arguments, e.g. `rtk git commit` or
    # `rtk rg -n git src`. Claude Code's worktree-isolation guard refuses those
    # in an EnterWorktree-isolated session, and judges the REWRITTEN command.
    # rtk's rewrite of `git ...` depends on Claude Code allow rules (exit 0 when
    # they cover the command), so this cannot be fixed in rtk config. Other
    # rewrites (rtk find, rtk read, rtk ls, ...) and every rewrite outside a
    # linked worktree are kept. Implemented in ceta's internal/inputproc
    # (isolation.go), so it also protects any later processor in the chain.
    phillipgreenii.programs.claude-extended-tool-approver.inputProcessors = lib.mkIf cetaCfg.enable [
      "rtk rewrite"
    ];
  };
}

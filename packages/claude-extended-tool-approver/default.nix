{
  lib,
  buildGoModule,
}:

buildGoModule {
  pname = "claude-extended-tool-approver";
  version = "0.1.0";

  src = lib.cleanSource ./.;

  subPackages = [ "cmd/claude-extended-tool-approver" ];

  vendorHash = "sha256-qOWInVJQ9t9rODdzpKeVeFhJhuR3gEa76TV1g9OD/lg=";

  postInstall = ''
    mkdir -p $out/share/claude-extended-tool-approver/skills
    cp -r ${./skills}/* $out/share/claude-extended-tool-approver/skills/

    # Generate shell completions
    mkdir -p $out/share/bash-completion/completions
    mkdir -p $out/share/zsh/site-functions
    mkdir -p $out/share/fish/vendor_completions.d
    $out/bin/claude-extended-tool-approver completion bash > $out/share/bash-completion/completions/claude-extended-tool-approver
    $out/bin/claude-extended-tool-approver completion zsh > $out/share/zsh/site-functions/_claude-extended-tool-approver
    $out/bin/claude-extended-tool-approver completion fish > $out/share/fish/vendor_completions.d/claude-extended-tool-approver.fish
  '';

  meta = {
    description = "Claude Code extended tool approval with rule-based permission evaluation and decision logging";
    mainProgram = "claude-extended-tool-approver";
  };
}

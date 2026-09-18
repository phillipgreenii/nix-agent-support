{
  lib,
  mkGoApp,
}:

mkGoApp {
  pname = "osx-bridge-api";

  # Pattern A (phillipg-nix-repo-base ADR 0008): a single module rooted at this
  # package dir, go.mod and the committed gomod2nix.toml side by side, no
  # local `replace` to a sibling package — go-eventkit is a plain third-party
  # dependency vendored via gomod2nix, not a workspace sibling. No modRoot
  # needed.
  src = lib.cleanSource ./.;
  modRoot = null;

  gomod2nixToml = ./gomod2nix.toml;

  # Pin the shipped binary to the single entrypoint. The full `go test ./...`
  # suite (AC #1's own gate) is run separately by the flake's
  # osx-bridge-api-go-tests check, the builder that deliberately does NOT set
  # subPackages — mirroring pg-ccaudit-go-tests/pg-router-go-tests's own split
  # (this repo's `package-versioning.md` path-rule: "a Go package with
  # `subPackages` set means `nix build .#<pkg>` compiles only `cmd/`").
  subPackages = [ "cmd/osx-bridge-api" ];

  # internal/eventkitprovider's darwin file links -framework EventKit/
  # Foundation/AppKit/CoreLocation via cgo (go-eventkit's own bridge_darwin.go
  # cgo directive); this repo's mkGoTest already threads CGO_ENABLED=1 for its
  # own race-detector needs and this repo's nixpkgs pin's Go already defaults
  # to CGO_ENABLED=1 on darwin (bead pg2-j7vgy), and the darwin stdenv's own
  # apple-sdk (bundled by default, no extra buildInputs) provides every
  # framework's headers/.tbd — confirmed empirically (`nix eval
  # nixpkgs#legacyPackages.aarch64-darwin.apple-sdk` resolves and its SDK tree
  # contains EventKit.framework) while implementing this package. On the
  # non-darwin systems this flake also evaluates for (aarch64-linux,
  # x86_64-linux), internal/eventkitprovider's OTHER file
  # (provider_other.go, //go:build !darwin) is selected instead — pure Go,
  # no cgo — so this package builds on every system the flake declares with
  # no per-system exclusion, mirroring go-eventkit's own
  # bridge_darwin.go/bridge_other.go split.

  meta = {
    description = "Shared macOS integration daemon (Calendar via EventKit today, extensible to Mail/Contacts/Reminders) — deployed as a launchd user agent for its own clean TCC identity";
    mainProgram = "osx-bridge-api";
    platforms = lib.platforms.unix;
  };
}

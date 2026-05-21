#!/usr/bin/env bash
# Delegates to the Nix-wrapped update-locks package (sources update-locks-lib from nix store)
exec nix run .#update-locks -- "$@"

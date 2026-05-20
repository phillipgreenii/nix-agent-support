#!/usr/bin/env bash
# Regenerates pa_monitor.pb.go and pa_monitor_grpc.pb.go from the .proto
# schema. Run from the package root: ./scripts/gen-proto.sh
#
# Requirements (provided via nix devShell): protoc, protoc-gen-go,
# protoc-gen-go-grpc.

set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v protoc >/dev/null; then
  echo "error: protoc not in PATH" >&2
  exit 1
fi

protoc \
  --go_out=. \
  --go_opt=paths=source_relative \
  --go-grpc_out=. \
  --go-grpc_opt=paths=source_relative \
  internal/proto/pa_monitor.proto

echo "Generated: internal/proto/pa_monitor.pb.go internal/proto/pa_monitor_grpc.pb.go"

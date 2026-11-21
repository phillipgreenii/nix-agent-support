module github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk

go 1.26.10

require (
	github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector v0.0.0
	github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-sqlite v0.0.0
	github.com/phillipgreenii/x v0.0.0-20261118145935-46d6423c44db
	github.com/prometheus/client_golang v1.24.1
	github.com/prometheus/client_model v0.6.3
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.1.0
	go.opentelemetry.io/contrib/bridges/otelslog v0.20.1
	go.opentelemetry.io/otel v1.47.0
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.22.0
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.22.0
	go.opentelemetry.io/otel/exporters/prometheus v0.56.0
	go.opentelemetry.io/otel/log v1.47.0
	go.opentelemetry.io/otel/metric v1.47.0
	go.opentelemetry.io/otel/sdk v1.47.0
	go.opentelemetry.io/otel/sdk/log v0.22.0
	go.opentelemetry.io/otel/sdk/metric v1.47.0
	golang.org/x/sys v0.50.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.62.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.34.0 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/phillipgreenii/pg-router v0.0.0
	github.com/prometheus/common v0.71.0 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/collector/featuregate v1.65.0 // indirect
	go.opentelemetry.io/collector/pdata v1.65.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.46.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.1 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261118205021-e6950dcf398d // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261118205021-e6950dcf398d // indirect
	google.golang.org/grpc v1.85.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/libc v1.82.2 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.13.0 // indirect
)

// pg-connector is a sibling module in this same repo: its published client
// package carries every call and decode of the pg-connector CLI, and
// internal/gather's own test suite reuses pkg/scriptout/conformance's real
// wire-envelope schema checker — resolved
// natively via a local replace, the same "local replace => ../sibling"
// pattern packages/pg-router-handlers's go.mod already uses for its
// own sibling-module dependencies (phillipg-nix-repo-base ADR 0008).
replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector => ../pg-connector

// pg-sqlite is the shared sqlite-open helper (ADR 0125), resolved by a local replace like pg-connector.
replace github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-sqlite => ../pg-sqlite

replace github.com/phillipgreenii/pg-router => ../pg-router

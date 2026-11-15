package main

import "time"

// Per-call bounds for the pg-connector (and pg-router) calls this package
// makes directly. internal/connector.Run supplies the process group and the
// graceful kill; the bound itself stays visible at each call site so the
// bounded-call guard (connector_bounded_test.go) can check it.
const (
	connectorCallTimeout   = 60 * time.Second
	connectorInjectTimeout = 30 * time.Second
)

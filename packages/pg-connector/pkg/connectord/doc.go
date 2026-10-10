// Package connectord is the shared library for a pg-connector daemon: the
// generic parts a stateful connector that runs a long-lived daemon builds on
// (store primitives and migrations, scheduler, governor, change log, socket
// server and client, process scaffolding), as subpackages such as store,
// governor, scheduler, socket and proc.
//
// Only the generic parts live here. Everything specific to one upstream
// (field groups, fetchers, classifier, query shapes) stays inside the
// connector binary's own cmd/<binary>/internal/ tree. A connector that adopts
// this library MUST scope its daemon to one upstream rate-limit domain, not to
// one connector type.
//
// This package and its subpackages are part of the module's shared-surface
// allowlist (cmd/pg-connector/layout_convention_test.go): any binary may
// import them, so they MUST NOT import a binary's internal/ tree or package
// main.
package connectord

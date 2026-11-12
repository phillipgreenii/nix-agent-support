//go:build !integration || !unix

package main

// keepRouterEnv reports whether this process is a re-executed router child that
// must keep its PG_ROUTER_* variables. The unit tier never re-executes itself.
func keepRouterEnv() bool { return false }

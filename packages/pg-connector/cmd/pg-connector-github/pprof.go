package main

// pprofEnv names the environment variable that opts the serve mode into the
// opt-in pprof endpoint. It is an operator diagnostic, not a setting: unset
// (the default) opens no listener, the config file never carries it, and the pr,
// ci and status modes never read it. The Home-Manager module's launchd
// registration sets it to a 0600 unix socket under the daemon's state
// directory (home/programs/pg-connector/default.nix, github.pprof.enable). The
// value rules (absolute socket path or loopback host:port, otherwise refused
// with one line on stderr) are pprofserver's, not re-implemented here.
const pprofEnv = "PG_CONNECTOR_GITHUB_PPROF_ADDR"

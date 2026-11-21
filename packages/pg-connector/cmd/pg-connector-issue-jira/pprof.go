package main

// pprofEnv names the environment variable that opts the serve mode into the
// opt-in pprof endpoint. It is an operator diagnostic, not a setting: unset
// (the default) opens no listener, the config file never carries it, and no
// other mode reads it. The value rules (absolute socket path or loopback
// host:port, otherwise refused with one line on stderr) are pprofserver's, not
// re-implemented here.
const pprofEnv = "PG_CONNECTOR_ISSUE_JIRA_PPROF_ADDR"

// Package config loads the rendered settings file of pg-connector-issue-jira,
// the ONE file that both the Jira daemon and its client mode read.
//
// # File format
//
// The file named by --config is ONE JSON object, rendered by the home-manager
// module from a nix attrset with builtins.toJSON. The loader reads no
// environment variable at all (no override, no fallback for a missing
// --config), so a daemon started by hand under env -i behaves as it does under
// its supervisor. An unknown key at any depth, or trailing data, is a decode
// error naming the key, so a typo never silently falls back to a default. A
// JSON null for any key means unset, so its default applies. testdata/example.json
// is the golden example with every key written out at its default; a file MAY
// carry only the keys it changes.
//
// Durations are strings with s, m, h or d units ("60s", "10m", "24h", "7d").
// change_body_cap is an integer of bytes or a string such as "4KiB".
//
// # Keys and defaults
//
// Required:
//
//	state_dir   absolute directory for the store, logs and lock
//	pjira_path  absolute path of the pjira binary (launchd gives the daemon no
//	            PATH, so the daemon execs pjira by this path only; checked only
//	            for being absolute, not for existing)
//
// Optional, with defaults:
//
//	socket_path             <state_dir>/sock (absolute; checked against the platform sun_path limit)
//	instance                pg-connector-issue-jira (the registry instance name reported as the source of feed answers)
//	supervisor_label        com.phillipg.pg-connector-issue-jira-daemon
//	otel_endpoint           absent or "" means no export
//	config_hash             the hash the renderer stamped (see "Config hash")
//	config_path             where the renderer installs the file (informational; Config.RenderedPath)
//	pjira_env               {} (see "pjira_env")
//	queries                 {} (see "Queries")
//	request_rate_per_second 2
//	concurrency             2
//	default_pause           60s (pause after a 429 that states no Retry-After)
//	rate_reserve_requests   20 (the floor: nothing spends below this many requests left in the origin's burst window; 0 refuses only at none left)
//	write_notice_timeout    500ms
//	write_lease             30s
//	summary_ttl             5m
//	detail_ttl              30m
//	detail_max_age          90m
//	query_ttl               {} (object from query name to duration)
//	query_ttl_default       5m  (for a query not named in query_ttl)
//	terminal_ttl            24h (refreshed until 24 hours after the issue closes)
//	expiry                  7d (evicted 7 days after closing)
//	tracked_set_alert_size  55
//	settle_timeout          30s
//	change_log_retention    14d
//	consumer_stale_after    7d
//	change_body_cap         4096 bytes
//	fill_horizon            one summary_ttl when unset or 0s
//	batch_size              74
//	workers                 the value of concurrency
//
// The 2 requests per second and concurrency 2 are the P1 spike output for the
// burst headroom, and 55 is the P1 spike's tracked-set threshold (five times
// the 11 Jira entities observed). The group, retention and change defaults
// start from the sibling GitHub daemon's constants of the same meaning; the
// design fixes no number for them. Zero or negative values are rejected for
// the rates, concurrency, workers, batch_size, write_lease and every TTL (and
// for the other durations and sizes). rate_reserve_requests is the one key
// where zero is valid (the floor holds nothing back); a negative value is rejected.
//
// # pjira_env
//
// pjira_env is an object from string to string and is the ONLY environment the
// daemon gives every pjira process it execs: the origin tool reads its tenant
// settings and credential source from its environment, and launchd gives the
// daemon none, so a daemon started by hand under env -i and one started by its
// supervisor call pjira identically. The design asks for an absolute pjira path
// because launchd supplies no PATH; carrying the environment the same way is
// this package's decision.
//
// # Queries
//
// queries maps a caller-facing query name to a schema.QueryDef: a bare string
// or a list of strings is an unwatched query, and {"expr": "e" | ["a", "b"],
// "watched": true} is a watched one. Nothing is watched by default. None is
// built in; the deployment supplies them.
//
// # Paths
//
// state_dir, socket_path and pjira_path MUST be absolute: a relative path would
// resolve against whatever directory started the process, so a daemon started
// by hand and one under a supervisor (working directory /) would differ. The
// state directory the home-manager module renders is
// $XDG_STATE_HOME/pg-connector-issue-jira-daemon (StateDirName), which differs
// from the old stateless backend's $XDG_STATE_HOME/pg-connector-issue-jira.
//
// # Config hash
//
// The renderer stamps the same hash into this file ("config_hash") and into
// every umbrella-side backends.<name> block ("config_hash" plus "config_path";
// see FieldConfigHash and FieldConfigPath). Load trusts the stamped hash and
// does not recompute it; a file with no stamp gets a deterministic sha256 over
// the effective settings. CheckBlock accepts a block carrying neither stamp
// and rejects one whose config_hash differs.
package config

package config

import "path/filepath"

// ResolvePath applies the config lookup order and returns the chosen path and
// where it came from:
//
//  1. --config PATH (flagPath)
//  2. $PG_RESCUE_CONFIG
//  3. ${XDG_CONFIG_HOME:-~/.config}/pg-rescue/config.toml
//
// An empty value counts as unset. The file is not required to exist here.
func ResolvePath(flagPath string, getenv func(string) string, home string) (path, source string) {
	if flagPath != "" {
		return flagPath, "--config"
	}
	if p := getenv("PG_RESCUE_CONFIG"); p != "" {
		return p, "$PG_RESCUE_CONFIG"
	}
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "pg-rescue", "config.toml"), "$XDG_CONFIG_HOME"
	}
	return filepath.Join(home, ".config", "pg-rescue", "config.toml"), "~/.config"
}

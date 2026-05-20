package main

import "github.com/phillipgreenii/pa-monitor/internal/config"

// configLoad is a thin wrapper so multiple subcommands share one loader.
func configLoad() (config.Config, error) {
	return config.Load(config.DefaultPath())
}

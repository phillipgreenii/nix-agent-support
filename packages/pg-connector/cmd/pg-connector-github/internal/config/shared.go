package config

import (
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/daemoncfg"
)

// The duration type and the backends-block stamp check are shared by every
// connector daemon: they live in pkg/connectord/daemoncfg and are re-exported
// here so the config's fields and callers keep their names.

// Duration is daemoncfg.Duration.
type Duration = daemoncfg.Duration

// ByteSize is daemoncfg.ByteSize.
type ByteSize = daemoncfg.ByteSize

// ErrConfigHashMismatch is daemoncfg.ErrConfigHashMismatch.
var ErrConfigHashMismatch = daemoncfg.ErrConfigHashMismatch

// BlockStamp is daemoncfg.BlockStamp.
func BlockStamp(block json.RawMessage) (hash, path string, err error) {
	return daemoncfg.BlockStamp(block)
}

// CheckBlock compares the stamp of an attached backends.<name> block to cfg.
func CheckBlock(cfg *Config, block json.RawMessage) error {
	return daemoncfg.CheckBlock(cfg.Hash, cfg.Path, block)
}

package config

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrConfigHashMismatch is wrapped by the error CheckBlock returns when an
// attached backends.<name> block carries a different hash than the loaded
// config. The caller answers invalid_argument.
var ErrConfigHashMismatch = errors.New("config hash mismatch")

// BlockStamp reads the stamp fields (FieldConfigHash, FieldConfigPath) from a
// request's attached backends.<name> block. A null or empty block, or one
// without either field, yields empty strings: it is accepted, never an error.
func BlockStamp(block json.RawMessage) (hash, path string, err error) {
	if len(block) == 0 || string(block) == "null" {
		return "", "", nil
	}
	var fields struct {
		Hash string `json:"config_hash"`
		Path string `json:"config_path"`
	}
	if err := json.Unmarshal(block, &fields); err != nil {
		// A non-object block is another module's problem; it carries no stamp.
		var anyVal any
		if json.Unmarshal(block, &anyVal) == nil {
			return "", "", nil
		}
		return "", "", fmt.Errorf("backends block: %w", err)
	}
	return fields.Hash, fields.Path, nil
}

// CheckBlock compares the stamp of an attached backends.<name> block to cfg. A
// block carrying no config_hash is accepted. A differing hash returns an error
// wrapping ErrConfigHashMismatch that names both config paths (the daemon's own
// and the block's, "unknown" when the block gave none).
func CheckBlock(cfg *Config, block json.RawMessage) error {
	hash, path, err := BlockStamp(block)
	if err != nil {
		return err
	}
	if hash == "" || hash == cfg.Hash {
		return nil
	}
	if path == "" {
		path = "unknown"
	}
	return fmt.Errorf("%w: the request's backends block was rendered from config %s (hash %s) but the daemon runs config %s (hash %s)",
		ErrConfigHashMismatch, path, hash, cfg.Path, cfg.Hash)
}

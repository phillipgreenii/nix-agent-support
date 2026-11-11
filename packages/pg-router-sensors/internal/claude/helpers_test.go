package claude

import "github.com/BurntSushi/toml"

func tomlDecode(s string, v any) error { _, err := toml.Decode(s, v); return err }

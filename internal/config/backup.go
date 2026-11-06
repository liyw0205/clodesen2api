package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// BackupConfig applies public TOML settings to a startup baseline for semantic
// validation. Credential fields stay private and do not change restore paths.
func BackupConfig(base Config, data []byte, overrides json.RawMessage) (Config, error) {
	var parsed map[string]map[string]any
	if len(data) > 0 {
		if err := toml.Unmarshal(data, &parsed); err != nil {
			return Config{}, err
		}
	}
	values := map[string]json.RawMessage{}
	for _, path := range settingNames {
		parts := strings.SplitN(path, ".", 2)
		var snake strings.Builder
		for i, c := range parts[1] {
			if c >= 'A' && c <= 'Z' && i > 0 {
				snake.WriteByte('_')
			}
			snake.WriteRune(c)
		}
		key := strings.ToLower(snake.String())
		switch parts[1] {
		case "baseURL":
			key = "base_url"
		case "openAIBaseURL":
			key = "openai_base_url"
		}
		if value, ok := parsed[parts[0]][key]; ok {
			raw, err := json.Marshal(value)
			if err != nil {
				return Config{}, err
			}
			values[path] = raw
		}
	}
	for name, original := range map[string]string{"token_file": base.Admin.TokenFile, "accounts_file": base.Admin.AccountsFile} {
		if value, ok := parsed["admin"][name]; ok && value != original {
			return Config{}, fmt.Errorf("restore cannot relocate admin.%s", name)
		}
	}
	merged, err := applyValues(base, values)
	if err != nil {
		return Config{}, err
	}
	if len(overrides) > 0 {
		var doc settingsDocument
		if err := json.Unmarshal(overrides, &doc); err != nil {
			return Config{}, err
		}
		if doc.Schema != 1 || doc.Values == nil {
			return Config{}, fmt.Errorf("invalid settings schema")
		}
		merged, err = applyValues(merged, doc.Values)
	}
	return merged, err
}

package config

import (
	"fmt"
	"os"

	"github.com/lcoder/lcoder/internal/fsutil"
	"github.com/lcoder/lcoder/internal/paths"
	"gopkg.in/yaml.v3"
)

// LoadCredentials reads a credentials.yaml mapping provider name to connection
// settings (api_key plus optional base_url/route/headers). A missing file
// returns an empty map (not an error).
func LoadCredentials(path string) (map[string]ProviderConn, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]ProviderConn{}, nil
		}
		return nil, fmt.Errorf("read credentials %s: %w", path, err)
	}
	var creds map[string]ProviderConn
	if err := yaml.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials %s: %w", path, err)
	}
	if creds == nil {
		creds = map[string]ProviderConn{}
	}
	return creds, nil
}

// resolveCredentialsPath returns ~/.lcoder/credentials.yaml (empty if no home).
func resolveCredentialsPath() string {
	return paths.LCoderHome("credentials.yaml")
}

// CredentialsPath is the exported accessor for the TUI-managed credentials file
// (~/.lcoder/credentials.yaml). Returns "" when the home dir is unavailable.
func CredentialsPath() string {
	return resolveCredentialsPath()
}

// ProviderHasKey reports whether the given provider already has a usable api key,
// checking the merged config.providers map first (which Load() folds credentials
// into) and then the provider's standard environment variable from the built-in
// table. Used to decide whether the first-launch wizard should fire.
func ProviderHasKey(cfg Config, provider string) bool {
	if conn, ok := cfg.Providers[provider]; ok && conn.APIKey != "" {
		return true
	}
	if info, ok := BuiltinProvider(provider); ok && info.KeyEnv != "" {
		if os.Getenv(info.KeyEnv) != "" {
			return true
		}
	}
	return false
}

// mergeCredentials folds creds into providers without overriding fields already
// set in providers — hand-written config.providers wins over TUI credentials.
func mergeCredentials(providers, creds map[string]ProviderConn) map[string]ProviderConn {
	if len(creds) == 0 {
		return providers
	}
	if providers == nil {
		providers = map[string]ProviderConn{}
	}
	for name, cred := range creds {
		existing, ok := providers[name]
		if !ok {
			providers[name] = cred
			continue
		}
		if existing.APIKey == "" {
			existing.APIKey = cred.APIKey
		}
		if existing.BaseURL == "" {
			existing.BaseURL = cred.BaseURL
		}
		if existing.Route == "" {
			existing.Route = cred.Route
		}
		if existing.Headers == nil {
			existing.Headers = cred.Headers
		}
		if len(existing.APIKeys) == 0 {
			existing.APIKeys = cred.APIKeys
		}
		if existing.Protocol == "" {
			existing.Protocol = cred.Protocol
		}
		if existing.MaxConcurrent == 0 {
			existing.MaxConcurrent = cred.MaxConcurrent
		}
		providers[name] = existing
	}
	return providers
}

// SaveCredentials writes creds to path with 0600 permissions, creating the
// parent directory as needed. Used by the TUI to persist entered api keys.
func SaveCredentials(path string, creds map[string]ProviderConn) error {
	data, err := yaml.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if err := fsutil.WritePrivateFile(path, data); err != nil {
		return fmt.Errorf("write credentials %s: %w", path, err)
	}
	return nil
}

// configFilePath returns ~/.lcoder/config.yaml (empty when no home dir).
func configFilePath() string {
	return paths.LCoderHome("config.yaml")
}

// SaveProviderSelection persists the chosen provider and model id to the main
// config file (~/.lcoder/config.yaml), preserving every other existing key. The
// TUI provider wizard calls this so the selection survives the next launch: on
// restart Load() reads this provider (whose api key now lives in
// credentials.yaml), so ProviderHasKey reports true and the first-launch wizard
// does not re-fire for an already-configured provider.
func SaveProviderSelection(provider, model string) error {
	path := configFilePath()
	if path == "" {
		return fmt.Errorf("cannot resolve home directory for config path")
	}
	raw := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
		if raw == nil {
			raw = map[string]any{}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	raw["provider"] = provider
	raw["model"] = model
	data, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := fsutil.WritePrivateFile(path, data); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

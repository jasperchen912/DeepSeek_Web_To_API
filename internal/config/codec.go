package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

func (c Config) MarshalJSON() ([]byte, error) {
	m := map[string]any{}
	for k, v := range c.AdditionalFields {
		m[k] = v
	}
	if len(c.Keys) > 0 {
		m["keys"] = c.Keys
	}
	if len(c.APIKeys) > 0 {
		m["api_keys"] = c.APIKeys
	}
	if len(c.Accounts) > 0 {
		m["accounts"] = c.Accounts
	}
	if len(c.Proxies) > 0 {
		m["proxies"] = c.Proxies
	}
	if len(c.ModelAliases) > 0 {
		m["model_aliases"] = c.ModelAliases
	}
	if strings.TrimSpace(c.Admin.Key) != "" || strings.TrimSpace(c.Admin.PasswordHash) != "" || strings.TrimSpace(c.Admin.JWTSecret) != "" || c.Admin.JWTExpireHours > 0 || c.Admin.JWTValidAfterUnix > 0 {
		m["admin"] = c.Admin
	}
	if strings.TrimSpace(c.Server.Port) != "" || strings.TrimSpace(c.Server.BindAddr) != "" || strings.TrimSpace(c.Server.LogLevel) != "" || strings.TrimSpace(c.Server.StaticAdminDir) != "" || c.Server.AutoBuildWebUI != nil || c.Server.HTTPTotalTimeoutSeconds > 0 {
		m["server"] = c.Server
	}
	if strings.TrimSpace(c.Storage.DataDir) != "" ||
		strings.TrimSpace(c.Storage.AccountsSQLitePath) != "" ||
		strings.TrimSpace(c.Storage.ChatHistoryPath) != "" ||
		strings.TrimSpace(c.Storage.ChatHistorySQLitePath) != "" ||
		strings.TrimSpace(c.Storage.RawStreamSampleRoot) != "" {
		m["storage"] = c.Storage
	}
	if cacheConfigured(c.Cache) {
		m["cache"] = c.Cache
	}
	if c.Runtime.AccountMaxInflight > 0 || c.Runtime.AccountMaxQueue > 0 || c.Runtime.GlobalMaxInflight > 0 || c.Runtime.TokenRefreshIntervalHours > 0 {
		m["runtime"] = c.Runtime
	}
	if c.Compat.WideInputStrictOutput != nil || c.Compat.StripReferenceMarkers != nil {
		m["compat"] = c.Compat
	}
	if c.Responses.StoreTTLSeconds > 0 {
		m["responses"] = c.Responses
	}
	if strings.TrimSpace(c.Embeddings.Provider) != "" {
		m["embeddings"] = c.Embeddings
	}
	m["auto_delete"] = c.AutoDelete
	if c.HistorySplit.Enabled != nil || c.HistorySplit.TriggerAfterTurns != nil {
		m["history_split"] = c.HistorySplit
	}
	if c.CurrentInputFile.Enabled != nil || c.CurrentInputFile.MinChars != 0 {
		m["current_input_file"] = c.CurrentInputFile
	}
	if c.ThinkingInjection.Enabled != nil || strings.TrimSpace(c.ThinkingInjection.Prompt) != "" {
		m["thinking_injection"] = c.ThinkingInjection
	}
	return json.Marshal(m)
}

func (c *Config) UnmarshalJSON(b []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	c.AdditionalFields = map[string]any{}
	for k, v := range raw {
		switch k {
		case "keys":
			if err := json.Unmarshal(v, &c.Keys); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "api_keys":
			if err := json.Unmarshal(v, &c.APIKeys); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "accounts":
			if err := json.Unmarshal(v, &c.Accounts); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "proxies":
			if err := json.Unmarshal(v, &c.Proxies); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "claude_mapping":
		case "claude_model_mapping":
			// Removed legacy mapping fields are ignored instead of persisted.
		case "model_aliases":
			if err := json.Unmarshal(v, &c.ModelAliases); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "admin":
			if err := json.Unmarshal(v, &c.Admin); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "server":
			if err := json.Unmarshal(v, &c.Server); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "storage":
			if err := json.Unmarshal(v, &c.Storage); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "cache":
			if err := json.Unmarshal(v, &c.Cache); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "runtime":
			if err := json.Unmarshal(v, &c.Runtime); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "compat":
			if err := json.Unmarshal(v, &c.Compat); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "toolcall":
			// Legacy field ignored. Toolcall policy is fixed and no longer configurable.
		case "responses":
			if err := json.Unmarshal(v, &c.Responses); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "embeddings":
			if err := json.Unmarshal(v, &c.Embeddings); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "auto_delete":
			if err := json.Unmarshal(v, &c.AutoDelete); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "history_split":
			if err := json.Unmarshal(v, &c.HistorySplit); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "current_input_file":
			if err := json.Unmarshal(v, &c.CurrentInputFile); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		case "thinking_injection":
			if err := json.Unmarshal(v, &c.ThinkingInjection); err != nil {
				return fmt.Errorf("invalid field %q: %w", k, err)
			}
		default:
			var anyVal any
			if err := json.Unmarshal(v, &anyVal); err == nil {
				c.AdditionalFields[k] = anyVal
			}
		}
	}
	c.NormalizeCredentials()
	return nil
}

func (c Config) Clone() Config {
	clone := Config{
		Keys:         slices.Clone(c.Keys),
		APIKeys:      slices.Clone(c.APIKeys),
		Accounts:     slices.Clone(c.Accounts),
		Proxies:      slices.Clone(c.Proxies),
		ModelAliases: cloneStringMap(c.ModelAliases),
		Admin:        c.Admin,
		Server: ServerConfig{
			Port:                    c.Server.Port,
			BindAddr:                c.Server.BindAddr,
			LogLevel:                c.Server.LogLevel,
			StaticAdminDir:          c.Server.StaticAdminDir,
			AutoBuildWebUI:          cloneBoolPtr(c.Server.AutoBuildWebUI),
			HTTPTotalTimeoutSeconds: c.Server.HTTPTotalTimeoutSeconds,
		},
		Storage: c.Storage,
		Cache: CacheConfig{
			Response: c.Cache.Response,
			Session: SessionCacheConfig{
				Enabled:    cloneBoolPtr(c.Cache.Session.Enabled),
				TTLSeconds: c.Cache.Session.TTLSeconds,
				MaxEntries: c.Cache.Session.MaxEntries,
			},
		},
		Runtime: c.Runtime,
		Compat: CompatConfig{
			WideInputStrictOutput: cloneBoolPtr(c.Compat.WideInputStrictOutput),
			StripReferenceMarkers: cloneBoolPtr(c.Compat.StripReferenceMarkers),
		},
		Responses:  c.Responses,
		Embeddings: c.Embeddings,
		AutoDelete: c.AutoDelete,
		HistorySplit: HistorySplitConfig{
			Enabled:           cloneBoolPtr(c.HistorySplit.Enabled),
			TriggerAfterTurns: cloneIntPtr(c.HistorySplit.TriggerAfterTurns),
		},
		CurrentInputFile: CurrentInputFileConfig{
			Enabled:  cloneBoolPtr(c.CurrentInputFile.Enabled),
			MinChars: c.CurrentInputFile.MinChars,
		},
		ThinkingInjection: ThinkingInjectionConfig{
			Enabled: cloneBoolPtr(c.ThinkingInjection.Enabled),
			Prompt:  c.ThinkingInjection.Prompt,
		},
		AdditionalFields: map[string]any{},
	}
	for k, v := range c.AdditionalFields {
		clone.AdditionalFields[k] = v
	}
	return clone
}

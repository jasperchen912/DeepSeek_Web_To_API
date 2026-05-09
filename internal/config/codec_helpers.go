package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func responseCacheConfigured(c ResponseCacheConfig) bool {
	return strings.TrimSpace(c.Dir) != "" ||
		c.MemoryTTLSeconds > 0 ||
		c.DiskTTLSeconds > 0 ||
		c.MaxBodyBytes > 0 ||
		c.MemoryMaxBytes > 0 ||
		c.DiskMaxBytes > 0
}

func sessionCacheConfigured(c SessionCacheConfig) bool {
	return c.Enabled != nil ||
		c.TTLSeconds > 0 ||
		c.MaxEntries > 0
}

func cacheConfigured(c CacheConfig) bool {
	return responseCacheConfigured(c.Response) || sessionCacheConfigured(c.Session)
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneBoolPtr(in *bool) *bool {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func cloneIntPtr(in *int) *int {
	if in == nil {
		return nil
	}
	v := *in
	return &v
}

func parseConfigString(raw string) (Config, error) {
	var cfg Config
	candidates := []string{raw}
	if normalized := normalizeConfigInput(raw); normalized != raw {
		candidates = append(candidates, normalized)
	}
	for _, candidate := range candidates {
		if err := json.Unmarshal([]byte(candidate), &cfg); err == nil {
			return cfg, nil
		}
	}

	base64Input := candidates[len(candidates)-1]
	decoded, err := decodeConfigBase64(base64Input)
	if err != nil {
		return Config{}, fmt.Errorf("invalid DEEPSEEK_WEB_TO_API_CONFIG_JSON: %w", err)
	}
	if err := json.Unmarshal(decoded, &cfg); err != nil {
		return Config{}, fmt.Errorf("invalid DEEPSEEK_WEB_TO_API_CONFIG_JSON decoded JSON: %w", err)
	}
	return cfg, nil
}

func normalizeConfigInput(raw string) string {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return normalized
	}
	for {
		changed := false
		if len(normalized) >= 2 {
			first := normalized[0]
			last := normalized[len(normalized)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				normalized = strings.TrimSpace(normalized[1 : len(normalized)-1])
				changed = true
			}
		}
		if strings.HasPrefix(strings.ToLower(normalized), "base64:") {
			normalized = strings.TrimSpace(normalized[len("base64:"):])
			changed = true
		}
		if !changed {
			break
		}
	}
	return strings.TrimSpace(normalized)
}

func decodeConfigBase64(raw string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(raw)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("base64 decode failed")
}

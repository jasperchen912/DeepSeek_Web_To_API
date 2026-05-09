package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *Store) ServerPort() string {
	if raw := strings.TrimSpace(os.Getenv("PORT")); raw != "" {
		return raw
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if raw := strings.TrimSpace(s.cfg.Server.Port); raw != "" {
		return raw
	}
	return "5001"
}

func (s *Store) ServerBindAddr() string {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_BIND_ADDR")); raw != "" {
		return raw
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if raw := strings.TrimSpace(s.cfg.Server.BindAddr); raw != "" {
		return raw
	}
	return "0.0.0.0"
}

func (s *Store) ServerLogLevel() string {
	if raw := strings.TrimSpace(os.Getenv("LOG_LEVEL")); raw != "" {
		return raw
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if raw := strings.TrimSpace(s.cfg.Server.LogLevel); raw != "" {
		return raw
	}
	return "INFO"
}

func (s *Store) ServerAutoBuildWebUI() bool {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_AUTO_BUILD_WEBUI")); raw != "" {
		return parseBoolDefault(raw, true)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Server.AutoBuildWebUI != nil {
		return *s.cfg.Server.AutoBuildWebUI
	}
	return true
}

func (s *Store) HTTPTotalTimeout() time.Duration {
	s.mu.RLock()
	configured := s.cfg.Server.HTTPTotalTimeoutSeconds
	s.mu.RUnlock()
	return durationSecondsFromEnvOrConfig("DEEPSEEK_WEB_TO_API_HTTP_TOTAL_TIMEOUT_SECONDS", configured, defaultHTTPTotalTimeoutSeconds)
}

func (s *Store) StaticAdminDir() string {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_STATIC_ADMIN_DIR")); raw != "" {
		return resolvePathValue(raw, "static/admin")
	}
	s.mu.RLock()
	configured := s.cfg.Server.StaticAdminDir
	s.mu.RUnlock()
	if strings.TrimSpace(configured) != "" {
		return resolvePathValue(configured, "static/admin")
	}
	return StaticAdminDir()
}

func (s *Store) ChatHistoryPath() string {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_CHAT_HISTORY_PATH")); raw != "" {
		return resolvePathValue(raw, "data/chat_history.json")
	}
	s.mu.RLock()
	dataDir := s.cfg.Storage.DataDir
	configured := s.cfg.Storage.ChatHistoryPath
	s.mu.RUnlock()
	if strings.TrimSpace(configured) != "" {
		return resolvePathValue(configured, "data/chat_history.json")
	}
	if strings.TrimSpace(dataDir) != "" {
		return filepath.Join(resolvePathValue(dataDir, "data"), "chat_history.json")
	}
	return ChatHistoryPath()
}

func (s *Store) AccountsSQLitePath() string {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_ACCOUNTS_SQLITE_PATH")); raw != "" {
		return resolvePathValue(raw, "data/accounts.sqlite")
	}
	s.mu.RLock()
	dataDir := s.cfg.Storage.DataDir
	configured := s.cfg.Storage.AccountsSQLitePath
	s.mu.RUnlock()
	if strings.TrimSpace(configured) != "" {
		return resolvePathValue(configured, "data/accounts.sqlite")
	}
	if strings.TrimSpace(dataDir) != "" {
		return filepath.Join(resolvePathValue(dataDir, "data"), "accounts.sqlite")
	}
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_CONFIG_PATH")); raw != "" {
		return filepath.Join(filepath.Dir(resolvePathValue(raw, "config.json")), "accounts.sqlite")
	}
	return AccountsSQLitePath()
}

func (s *Store) ChatHistorySQLitePath() string {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_CHAT_HISTORY_SQLITE_PATH")); raw != "" {
		return resolvePathValue(raw, "data/chat_history.sqlite")
	}
	s.mu.RLock()
	dataDir := s.cfg.Storage.DataDir
	configured := s.cfg.Storage.ChatHistorySQLitePath
	s.mu.RUnlock()
	if strings.TrimSpace(configured) != "" {
		return resolvePathValue(configured, "data/chat_history.sqlite")
	}
	if strings.TrimSpace(dataDir) != "" {
		return filepath.Join(resolvePathValue(dataDir, "data"), "chat_history.sqlite")
	}
	return resolvePathValue("data/chat_history.sqlite", "data/chat_history.sqlite")
}

func (s *Store) RawStreamSampleRoot() string {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_RAW_STREAM_SAMPLE_ROOT")); raw != "" {
		return resolvePathValue(raw, "tests/raw_stream_samples")
	}
	s.mu.RLock()
	configured := s.cfg.Storage.RawStreamSampleRoot
	s.mu.RUnlock()
	if strings.TrimSpace(configured) != "" {
		return resolvePathValue(configured, "tests/raw_stream_samples")
	}
	return RawStreamSampleRoot()
}

func (s *Store) ResponseCacheDir() string {
	if raw := strings.TrimSpace(os.Getenv("DEEPSEEK_WEB_TO_API_RESPONSE_CACHE_DIR")); raw != "" {
		return resolvePathValue(raw, "data/response_cache")
	}
	s.mu.RLock()
	dataDir := s.cfg.Storage.DataDir
	configured := s.cfg.Cache.Response.Dir
	s.mu.RUnlock()
	if strings.TrimSpace(configured) != "" {
		return resolvePathValue(configured, "data/response_cache")
	}
	if strings.TrimSpace(dataDir) != "" {
		return filepath.Join(resolvePathValue(dataDir, "data"), "response_cache")
	}
	return ResponseCacheDir()
}

func (s *Store) ResponseCacheMemoryTTL() time.Duration {
	s.mu.RLock()
	seconds := s.cfg.Cache.Response.MemoryTTLSeconds
	s.mu.RUnlock()
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (s *Store) ResponseCacheDiskTTL() time.Duration {
	s.mu.RLock()
	seconds := s.cfg.Cache.Response.DiskTTLSeconds
	s.mu.RUnlock()
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (s *Store) ResponseCacheMaxBodyBytes() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Cache.Response.MaxBodyBytes
}

func (s *Store) ResponseCacheMemoryMaxBytes() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Cache.Response.MemoryMaxBytes
}

func (s *Store) ResponseCacheDiskMaxBytes() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Cache.Response.DiskMaxBytes
}

func (s *Store) SessionCacheEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Cache.Session.Enabled == nil {
		return true
	}
	return *s.cfg.Cache.Session.Enabled
}

func (s *Store) SessionCacheTTL() time.Duration {
	s.mu.RLock()
	seconds := s.cfg.Cache.Session.TTLSeconds
	s.mu.RUnlock()
	if seconds <= 0 {
		seconds = 7200
	}
	return time.Duration(seconds) * time.Second
}

func (s *Store) SessionCacheMaxEntries() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg.Cache.Session.MaxEntries > 0 {
		return s.cfg.Cache.Session.MaxEntries
	}
	return 50000
}

func resolvePathValue(raw, defaultRel string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultRel
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	return filepath.Join(BaseDir(), raw)
}

func durationSecondsFromEnvOrConfig(envKey string, configuredSeconds int, defaultSeconds int) time.Duration {
	if raw := strings.TrimSpace(os.Getenv(envKey)); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		return time.Duration(defaultSeconds) * time.Second
	}
	if configuredSeconds > 0 {
		return time.Duration(configuredSeconds) * time.Second
	}
	return time.Duration(defaultSeconds) * time.Second
}

func parseBoolDefault(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

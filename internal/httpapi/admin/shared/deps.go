package shared

import (
	"context"
	"net/http"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/promptcache"
	"DeepSeek_Web_To_API/internal/sessioncache"
)

type ConfigStore interface {
	Snapshot() config.Config
	Keys() []string
	Accounts() []config.Account
	FindAccount(identifier string) (config.Account, bool)
	UpdateAccountToken(identifier, token string) error
	UpdateAccountTestStatus(identifier, status string) error
	AccountTestStatus(identifier string) (string, bool)
	UpdateAccountSessionCount(identifier string, count int) error
	AccountSessionCount(identifier string) (int, bool)
	Update(mutator func(*config.Config) error) error
	ExportJSONAndBase64() (string, string, error)
	IsEnvBacked() bool
	IsEnvWritebackEnabled() bool
	HasEnvConfigSource() bool
	ConfigPath() string
	AdminKey() string
	AdminPasswordHash() string
	AdminJWTSecret() string
	AdminJWTExpireHours() int
	AdminJWTValidAfterUnix() int64
	RuntimeAccountMaxInflight() int
	RuntimeAccountMaxQueue(defaultSize int) int
	RuntimeGlobalMaxInflight(defaultSize int) int
	RuntimeTokenRefreshIntervalHours() int
	AutoDeleteMode() string
	HistorySplitEnabled() bool
	HistorySplitTriggerAfterTurns() int
	CurrentInputFileEnabled() bool
	CurrentInputFileMinChars() int
	ThinkingInjectionEnabled() bool
	ThinkingInjectionPrompt() string
	CompatStripReferenceMarkers() bool
	AutoDeleteSessions() bool
	RawStreamSampleRoot() string
}

type PoolController interface {
	Reset()
	Status() map[string]any
	ApplyRuntimeLimits(maxInflightPerAccount, maxQueueSize, globalMaxInflight int)
}

type ResponseCacheStatsProvider interface {
	Stats() map[string]any
}

type SessionCacheStatsProvider interface {
	Stats() sessioncache.Stats
}

type PromptCacheStatsProvider interface {
	Stats() promptcache.Stats
}

type OpenAIChatCaller interface {
	ChatCompletions(w http.ResponseWriter, r *http.Request)
}

type DeepSeekCaller interface {
	Login(ctx context.Context, acc config.Account) (string, error)
	CreateSession(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	GetPow(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	CallCompletion(ctx context.Context, a *auth.RequestAuth, payload map[string]any, powResp string, maxAttempts int) (*http.Response, error)
	GetSessionCountForToken(ctx context.Context, token string) (*dsclient.SessionStats, error)
	DeleteAllSessionsForToken(ctx context.Context, token string) error
}

var _ ConfigStore = (*config.Store)(nil)
var _ PoolController = (*account.Pool)(nil)
var _ DeepSeekCaller = (*dsclient.Client)(nil)

package metrics

import (
	"DeepSeek_Web_To_API/internal/chathistory"
	adminshared "DeepSeek_Web_To_API/internal/httpapi/admin/shared"
)

type Handler struct {
	Store         adminshared.ConfigStore
	Pool          adminshared.PoolController
	ChatHistory   *chathistory.Store
	ResponseCache adminshared.ResponseCacheStatsProvider
	SessionCache  adminshared.SessionCacheStatsProvider
	PromptCache   adminshared.PromptCacheStatsProvider
}

var writeJSON = adminshared.WriteJSON

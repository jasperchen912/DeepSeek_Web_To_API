package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/httpapi/admin"
	"DeepSeek_Web_To_API/internal/httpapi/claude"
	"DeepSeek_Web_To_API/internal/httpapi/gemini"
	"DeepSeek_Web_To_API/internal/httpapi/openai/chat"
	"DeepSeek_Web_To_API/internal/httpapi/openai/embeddings"
	"DeepSeek_Web_To_API/internal/httpapi/openai/files"
	"DeepSeek_Web_To_API/internal/httpapi/openai/responses"
	"DeepSeek_Web_To_API/internal/httpapi/openai/shared"
	"DeepSeek_Web_To_API/internal/httpapi/requestbody"
	"DeepSeek_Web_To_API/internal/promptcache"
	"DeepSeek_Web_To_API/internal/responsecache"
	"DeepSeek_Web_To_API/internal/sessioncache"
	"DeepSeek_Web_To_API/internal/webui"
)

type App struct {
	Store    *config.Store
	Pool     *account.Pool
	Resolver *auth.Resolver
	DS       *dsclient.Client
	Router   http.Handler
}

func NewApp() (*App, error) {
	store, err := config.LoadStoreWithError()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	pool := account.NewPool(store)
	var dsClient *dsclient.Client
	resolver := auth.NewResolver(store, pool, func(ctx context.Context, acc config.Account) (string, error) {
		return dsClient.Login(ctx, acc)
	})
	dsClient = dsclient.NewClient(store, resolver)
	if err := dsClient.PreloadPow(context.Background()); err != nil {
		config.Logger.Warn("[PoW] init failed", "error", err)
	} else {
		config.Logger.Info("[PoW] pure Go solver ready")
	}
	chatHistoryStore := chathistory.NewSQLite(store.ChatHistorySQLitePath(), store.ChatHistoryPath())
	if err := chatHistoryStore.Err(); err != nil {
		config.Logger.Warn("[chat_history] unavailable", "path", chatHistoryStore.Path(), "error", err)
	}
	protocolSessionCache := sessioncache.New(sessioncache.Options{
		TTL:        store.SessionCacheTTL(),
		MaxEntries: store.SessionCacheMaxEntries(),
	})
	protocolPromptCache := promptcache.New(promptcache.Options{})

	modelsHandler := &shared.ModelsHandler{Store: store}
	chatHandler := &chat.Handler{Store: store, Auth: resolver, DS: dsClient, ChatHistory: chatHistoryStore, SessionCache: protocolSessionCache, PromptCache: protocolPromptCache}
	responsesHandler := &responses.Handler{Store: store, Auth: resolver, DS: dsClient, ChatHistory: chatHistoryStore, SessionCache: protocolSessionCache, PromptCache: protocolPromptCache}
	filesHandler := &files.Handler{Store: store, Auth: resolver, DS: dsClient, ChatHistory: chatHistoryStore}
	embeddingsHandler := &embeddings.Handler{Store: store, Auth: resolver, DS: dsClient, ChatHistory: chatHistoryStore}
	claudeHandler := &claude.Handler{Store: store, Auth: resolver, DS: dsClient, OpenAI: chatHandler, ChatHistory: chatHistoryStore, PromptCache: protocolPromptCache}
	geminiHandler := &gemini.Handler{Store: store, Auth: resolver, DS: dsClient, OpenAI: chatHandler}
	protocolResponseCache := responsecache.New(responsecache.Options{
		Dir:            store.ResponseCacheDir(),
		MemoryTTL:      store.ResponseCacheMemoryTTL(),
		DiskTTL:        store.ResponseCacheDiskTTL(),
		MaxBody:        store.ResponseCacheMaxBodyBytes(),
		MemoryMaxBytes: store.ResponseCacheMemoryMaxBytes(),
		DiskMaxBytes:   store.ResponseCacheDiskMaxBytes(),
		OnHit:          responsesHandler.OnProtocolResponseCacheHit,
	})
	adminHandler := &admin.Handler{Store: store, Pool: pool, DS: dsClient, OpenAI: chatHandler, ChatHistory: chatHistoryStore, ResponseCache: protocolResponseCache, SessionCache: protocolSessionCache, PromptCache: protocolPromptCache}
	webuiHandler := webui.NewHandler(store.StaticAdminDir())

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(filteredLogger())
	r.Use(middleware.Recoverer)
	r.Use(cors)
	r.Use(securityHeaders)
	r.Use(requestbody.ValidateJSONUTF8)
	r.Use(timeout(0))
	r.Use(protocolResponseCache.Middleware(resolver))

	healthzHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
	readyzHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}
	r.Get("/healthz", healthzHandler)
	r.Head("/healthz", healthzHandler)
	r.Get("/readyz", readyzHandler)
	r.Head("/readyz", readyzHandler)
	r.Get("/v1/models", modelsHandler.ListModels)
	r.Get("/v1/models/{model_id}", modelsHandler.GetModel)
	r.Post("/v1/chat/completions", chatHandler.ChatCompletions)
	r.Post("/v1/responses", responsesHandler.Responses)
	r.Get("/v1/responses/{response_id}", responsesHandler.GetResponseByID)
	r.Post("/v1/files", filesHandler.UploadFile)
	r.Post("/v1/embeddings", embeddingsHandler.Embeddings)
	// Some SDK wrappers append their own /v1 prefix even when users configure
	// a base URL that already ends in /v1. Keep these aliases in-process so the
	// session is still accepted instead of failing as a 404.
	r.Get("/v1/v1/models", modelsHandler.ListModels)
	r.Get("/v1/v1/models/{model_id}", modelsHandler.GetModel)
	r.Post("/v1/v1/chat/completions", chatHandler.ChatCompletions)
	r.Post("/v1/v1/responses", responsesHandler.Responses)
	r.Get("/v1/v1/responses/{response_id}", responsesHandler.GetResponseByID)
	r.Post("/v1/v1/files", filesHandler.UploadFile)
	r.Post("/v1/v1/embeddings", embeddingsHandler.Embeddings)
	// Root OpenAI aliases support clients configured with the bare DeepSeek_Web_To_API service URL.
	r.Get("/models", modelsHandler.ListModels)
	r.Get("/models/{model_id}", modelsHandler.GetModel)
	r.Post("/chat/completions", chatHandler.ChatCompletions)
	r.Post("/responses", responsesHandler.Responses)
	r.Get("/responses/{response_id}", responsesHandler.GetResponseByID)
	r.Post("/files", filesHandler.UploadFile)
	r.Post("/embeddings", embeddingsHandler.Embeddings)
	claude.RegisterRoutes(r, claudeHandler)
	gemini.RegisterRoutes(r, geminiHandler)
	r.Route("/admin", func(ar chi.Router) {
		admin.RegisterRoutes(ar, adminHandler)
	})
	webui.RegisterRoutes(r, webuiHandler)
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/admin/") && webuiHandler.HandleAdminFallback(w, req) {
			return
		}
		http.NotFound(w, req)
	})

	return &App{Store: store, Pool: pool, Resolver: resolver, DS: dsClient, Router: r}, nil
}

func timeout(d time.Duration) func(http.Handler) http.Handler {
	if d <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return middleware.Timeout(d)
}

func filteredLogger() func(http.Handler) http.Handler {
	color := !isWindowsRuntime()
	base := &middleware.DefaultLogFormatter{
		Logger:  log.New(os.Stdout, "", log.LstdFlags),
		NoColor: !color,
	}
	return middleware.RequestLogger(&filteredLogFormatter{base: base})
}

func isWindowsRuntime() bool {
	return runtime.GOOS == "windows"
}

type filteredLogFormatter struct {
	base *middleware.DefaultLogFormatter
}

func (f *filteredLogFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	if r != nil && r.Method == http.MethodGet {
		path := strings.TrimSpace(r.URL.Path)
		if path == "/admin/chat-history" || strings.HasPrefix(path, "/admin/chat-history/") {
			return noopLogEntry{}
		}
	}
	return f.base.NewLogEntry(r)
}

type noopLogEntry struct{}

func (noopLogEntry) Write(_ int, _ int, _ http.Header, _ time.Duration, _ interface{}) {}

func (noopLogEntry) Panic(_ interface{}, _ []byte) {}

var defaultCORSAllowHeaders = []string{
	"Content-Type",
	"Authorization",
	"X-API-Key",
	"X-DeepSeek-Web-To-API-Target-Account",
	"X-DeepSeek-Web-To-API-Source",
	"X-Ds2-Target-Account",
	"X-Ds2-Source",
	"X-Goog-Api-Key",
	"Anthropic-Version",
	"Anthropic-Beta",
}

var blockedCORSRequestHeaders = map[string]struct{}{
	"x-deepseek-web-to-api-internal-token":       {},
	"x-deepseek-web-to-api-session-affinity-key": {},
	"x-ds2-internal-token":                       {},
	"x-ds2-session-affinity-key":                 {},
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func setCORSHeaders(w http.ResponseWriter, r *http.Request) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		addVaryHeaderToken(w.Header(), "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
	w.Header().Set("Access-Control-Allow-Headers", buildCORSAllowHeaders(r))
	w.Header().Set("Access-Control-Max-Age", "600")
	addVaryHeaderToken(w.Header(), "Access-Control-Request-Headers")
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Access-Control-Request-Private-Network")), "true") {
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		addVaryHeaderToken(w.Header(), "Access-Control-Request-Private-Network")
	}
}

func buildCORSAllowHeaders(r *http.Request) string {
	names := make([]string, 0, len(defaultCORSAllowHeaders)+4)
	seen := make(map[string]struct{}, len(defaultCORSAllowHeaders)+4)
	for _, name := range defaultCORSAllowHeaders {
		appendCORSHeaderName(&names, seen, name)
	}
	if r == nil {
		return strings.Join(names, ", ")
	}
	for _, name := range splitCORSRequestHeaders(r.Header.Get("Access-Control-Request-Headers")) {
		appendCORSHeaderName(&names, seen, name)
	}
	return strings.Join(names, ", ")
}

func splitCORSRequestHeaders(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if !isValidCORSHeaderToken(name) {
			continue
		}
		if _, blocked := blockedCORSRequestHeaders[strings.ToLower(name)]; blocked {
			continue
		}
		out = append(out, name)
	}
	return out
}

func appendCORSHeaderName(dst *[]string, seen map[string]struct{}, name string) {
	name = strings.TrimSpace(name)
	if !isValidCORSHeaderToken(name) {
		return
	}
	key := strings.ToLower(name)
	if _, blocked := blockedCORSRequestHeaders[key]; blocked {
		return
	}
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	*dst = append(*dst, name)
}

func isValidCORSHeaderToken(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func addVaryHeaderToken(h http.Header, token string) {
	if h == nil {
		return
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	current := h.Values("Vary")
	seen := map[string]struct{}{}
	merged := make([]string, 0, len(current)+1)
	for _, value := range current {
		for _, part := range strings.Split(value, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, name)
		}
	}
	key := strings.ToLower(token)
	if _, ok := seen[key]; !ok {
		merged = append(merged, token)
	}
	h.Set("Vary", strings.Join(merged, ", "))
}

func WriteUnhandledError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "api_error", "message": "Internal Server Error", "detail": err.Error()}})
}

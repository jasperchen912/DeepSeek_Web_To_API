package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	"DeepSeek_Web_To_API/internal/sessioncache"
)

type sessionCacheAuthStub struct {
	mu    sync.Mutex
	auths []*auth.RequestAuth
	calls int
}

type disabledSessionCacheConfig struct {
	mockOpenAIConfig
}

func (disabledSessionCacheConfig) SessionCacheEnabled() bool { return false }
func (disabledSessionCacheConfig) SessionCacheTTL() time.Duration {
	return time.Hour
}
func (disabledSessionCacheConfig) SessionCacheMaxEntries() int { return 10 }

func (s *sessionCacheAuthStub) Determine(req *http.Request) (*auth.RequestAuth, error) {
	return s.peek(), nil
}

func (s *sessionCacheAuthStub) DetermineCaller(req *http.Request) (*auth.RequestAuth, error) {
	return s.peek(), nil
}

func (s *sessionCacheAuthStub) DetermineWithSession(req *http.Request, _ []byte) (*auth.RequestAuth, error) {
	return s.next(), nil
}

func (s *sessionCacheAuthStub) Release(_ *auth.RequestAuth) {}

func (s *sessionCacheAuthStub) peek() *auth.RequestAuth {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.auths) == 0 {
		return &auth.RequestAuth{DeepSeekToken: "token", CallerID: "caller", SessionKey: "session", TriedAccounts: map[string]bool{}}
	}
	return cloneRequestAuth(s.auths[0])
}

func (s *sessionCacheAuthStub) next() *auth.RequestAuth {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.auths) == 0 {
		return &auth.RequestAuth{DeepSeekToken: "token", CallerID: "caller", SessionKey: "session", TriedAccounts: map[string]bool{}}
	}
	idx := s.calls
	s.calls++
	if idx >= len(s.auths) {
		idx = len(s.auths) - 1
	}
	return cloneRequestAuth(s.auths[idx])
}

func cloneRequestAuth(a *auth.RequestAuth) *auth.RequestAuth {
	if a == nil {
		return nil
	}
	out := *a
	if a.TriedAccounts != nil {
		out.TriedAccounts = map[string]bool{}
		for k, v := range a.TriedAccounts {
			out.TriedAccounts[k] = v
		}
	}
	return &out
}

type sessionCacheDSStub struct {
	mu                   sync.Mutex
	createSessions       []string
	createDelay          time.Duration
	createCalls          int
	callSessions         []string
	failOnceSession      string
	failOnCall           int
	failedOnce           bool
	mutateFirstCall      bool
	deleteSingleCalls    int
	deleteAllCalls       int
	deletedSessionID     string
	completionStatusCode int
}

func (m *sessionCacheDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	if m.createDelay > 0 {
		time.Sleep(m.createDelay)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls++
	idx := m.createCalls - 1
	if idx >= 0 && idx < len(m.createSessions) {
		return m.createSessions[idx], nil
	}
	return "session-id", nil
}

func (m *sessionCacheDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (m *sessionCacheDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-id", Filename: "file.txt", Bytes: 1, Status: "uploaded"}, nil
}

func (m *sessionCacheDSStub) CallCompletion(_ context.Context, a *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	sessionID, _ := payload["chat_session_id"].(string)
	m.mu.Lock()
	m.callSessions = append(m.callSessions, sessionID)
	callNumber := len(m.callSessions)
	if m.failOnceSession == sessionID && !m.failedOnce && (m.failOnCall == 0 || m.failOnCall == callNumber) {
		m.failedOnce = true
		m.mu.Unlock()
		return nil, &dsclient.RequestFailure{Kind: dsclient.FailureUpstreamStatus, StatusCode: http.StatusNotFound, Message: "chat_session_id not found"}
	}
	if m.mutateFirstCall && len(m.callSessions) == 1 {
		a.AccountID = "acct-b"
		payload["chat_session_id"] = "session-b"
	}
	status := m.completionStatusCode
	if status == 0 {
		status = http.StatusOK
	}
	m.mu.Unlock()
	return makeOpenAISSEHTTPResponseWithStatus(status,
		`data: {"p":"response/content","v":"hello"}`,
		`data: [DONE]`,
	), nil
}

func (m *sessionCacheDSStub) DeleteSessionForToken(_ context.Context, _ string, sessionID string) (*dsclient.DeleteSessionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteSingleCalls++
	m.deletedSessionID = sessionID
	return &dsclient.DeleteSessionResult{SessionID: sessionID, Success: true}, nil
}

func (m *sessionCacheDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteAllCalls++
	return nil
}

func makeOpenAISSEHTTPResponseWithStatus(status int, lines ...string) *http.Response {
	resp := makeOpenAISSEHTTPResponse(lines...)
	resp.StatusCode = status
	return resp
}

func TestChatCompletionsReusesCachedSessionForDirectToken(t *testing.T) {
	cache := sessioncache.New(sessioncache.Options{TTL: time.Hour, MaxEntries: 10})
	authStub := &sessionCacheAuthStub{auths: []*auth.RequestAuth{
		{UseConfigToken: false, DeepSeekToken: "direct-token", CallerID: "caller", SessionKey: "session", TriedAccounts: map[string]bool{}},
	}}
	ds := &sessionCacheDSStub{createSessions: []string{"session-1"}}
	h := &Handler{Store: mockOpenAIConfig{wideInput: true}, Auth: authStub, DS: ds, SessionCache: cache}

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer direct-token")
		h.ChatCompletions(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}

	if ds.createCalls != 1 {
		t.Fatalf("CreateSession calls=%d want=1", ds.createCalls)
	}
	if got := strings.Join(ds.callSessions, ","); got != "session-1,session-1" {
		t.Fatalf("completion sessions=%s", got)
	}
	if stats := cache.Stats(); stats.Hits != 1 || stats.Stores == 0 {
		t.Fatalf("unexpected session cache stats: %#v", stats)
	}
}

func TestChatCompletionsSessionCacheCollapsesConcurrentCreate(t *testing.T) {
	cache := sessioncache.New(sessioncache.Options{TTL: time.Hour, MaxEntries: 10})
	authStub := &sessionCacheAuthStub{auths: []*auth.RequestAuth{
		{UseConfigToken: false, DeepSeekToken: "direct-token", CallerID: "caller", SessionKey: "session", TriedAccounts: map[string]bool{}},
	}}
	ds := &sessionCacheDSStub{createSessions: []string{"session-1"}, createDelay: 20 * time.Millisecond}
	h := &Handler{Store: mockOpenAIConfig{wideInput: true}, Auth: authStub, DS: ds, SessionCache: cache}

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer direct-token")
			h.ChatCompletions(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		}()
	}
	wg.Wait()

	if ds.createCalls != 1 {
		t.Fatalf("CreateSession calls=%d want=1", ds.createCalls)
	}
	if stats := cache.Stats(); stats.InflightWaits == 0 {
		t.Fatalf("expected inflight wait stats, got %#v", stats)
	}
}

func TestChatCompletionsSessionCacheBypassesAutoDelete(t *testing.T) {
	cache := sessioncache.New(sessioncache.Options{TTL: time.Hour, MaxEntries: 10})
	authStub := &sessionCacheAuthStub{auths: []*auth.RequestAuth{
		{UseConfigToken: false, DeepSeekToken: "direct-token", CallerID: "caller", SessionKey: "session", TriedAccounts: map[string]bool{}},
	}}
	ds := &sessionCacheDSStub{createSessions: []string{"session-1", "session-2"}}
	h := &Handler{Store: mockOpenAIConfig{wideInput: true, autoDeleteMode: "single"}, Auth: authStub, DS: ds, SessionCache: cache}

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer direct-token")
		h.ChatCompletions(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	if ds.createCalls != 2 || ds.deleteSingleCalls != 2 {
		t.Fatalf("create=%d delete=%d, want 2/2", ds.createCalls, ds.deleteSingleCalls)
	}
	if stats := cache.Stats(); stats.DisabledAutoDelete != 2 || stats.Hits != 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestChatCompletionsSessionCacheCanBeDisabled(t *testing.T) {
	cache := sessioncache.New(sessioncache.Options{TTL: time.Hour, MaxEntries: 10})
	authStub := &sessionCacheAuthStub{auths: []*auth.RequestAuth{
		{UseConfigToken: false, DeepSeekToken: "direct-token", CallerID: "caller", SessionKey: "session", TriedAccounts: map[string]bool{}},
	}}
	ds := &sessionCacheDSStub{createSessions: []string{"session-1", "session-2"}}
	h := &Handler{Store: disabledSessionCacheConfig{mockOpenAIConfig: mockOpenAIConfig{wideInput: true}}, Auth: authStub, DS: ds, SessionCache: cache}

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer direct-token")
		h.ChatCompletions(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	if ds.createCalls != 2 {
		t.Fatalf("CreateSession calls=%d want=2", ds.createCalls)
	}
	if stats := cache.Stats(); stats.Entries != 0 || stats.Hits != 0 {
		t.Fatalf("disabled cache should stay empty, got %#v", stats)
	}
}

func TestChatCompletionsCachedSessionInvalidatesAndRetries(t *testing.T) {
	cache := sessioncache.New(sessioncache.Options{TTL: time.Hour, MaxEntries: 10})
	authStub := &sessionCacheAuthStub{auths: []*auth.RequestAuth{
		{UseConfigToken: false, DeepSeekToken: "direct-token", CallerID: "caller", SessionKey: "session", TriedAccounts: map[string]bool{}},
	}}
	ds := &sessionCacheDSStub{createSessions: []string{"session-1", "session-2"}, failOnceSession: "session-1", failOnCall: 2}
	h := &Handler{Store: mockOpenAIConfig{wideInput: true}, Auth: authStub, DS: ds, SessionCache: cache}

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer direct-token")
		h.ChatCompletions(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}

	if ds.createCalls != 2 {
		t.Fatalf("CreateSession calls=%d want=2", ds.createCalls)
	}
	if got := strings.Join(ds.callSessions, ","); got != "session-1,session-1,session-2" {
		t.Fatalf("completion sessions=%s", got)
	}
	if stats := cache.Stats(); stats.Invalidations == 0 {
		t.Fatalf("expected invalidation, got %#v", stats)
	}
}

func TestChatCompletionsNewSessionInvalidatesAndRetries(t *testing.T) {
	cache := sessioncache.New(sessioncache.Options{TTL: time.Hour, MaxEntries: 10})
	authStub := &sessionCacheAuthStub{auths: []*auth.RequestAuth{
		{UseConfigToken: false, DeepSeekToken: "direct-token", CallerID: "caller", SessionKey: "session", TriedAccounts: map[string]bool{}},
	}}
	ds := &sessionCacheDSStub{createSessions: []string{"session-1", "session-2"}, failOnceSession: "session-1", failOnCall: 1}
	h := &Handler{Store: mockOpenAIConfig{wideInput: true}, Auth: authStub, DS: ds, SessionCache: cache}

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer direct-token")
	h.ChatCompletions(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	if ds.createCalls != 2 {
		t.Fatalf("CreateSession calls=%d want=2", ds.createCalls)
	}
	if got := strings.Join(ds.callSessions, ","); got != "session-1,session-2" {
		t.Fatalf("completion sessions=%s", got)
	}
	if stats := cache.Stats(); stats.Invalidations == 0 {
		t.Fatalf("expected invalidation, got %#v", stats)
	}
}

func TestChatCompletionsStoresFinalSwitchedAccountSession(t *testing.T) {
	cache := sessioncache.New(sessioncache.Options{TTL: time.Hour, MaxEntries: 10})
	authStub := &sessionCacheAuthStub{auths: []*auth.RequestAuth{
		{UseConfigToken: true, DeepSeekToken: "token-a", CallerID: "caller", AccountID: "acct-a", SessionKey: "session", TriedAccounts: map[string]bool{}},
		{UseConfigToken: true, DeepSeekToken: "token-b", CallerID: "caller", AccountID: "acct-b", SessionKey: "session", TriedAccounts: map[string]bool{}},
	}}
	ds := &sessionCacheDSStub{createSessions: []string{"session-a"}, mutateFirstCall: true}
	h := &Handler{Store: mockOpenAIConfig{wideInput: true}, Auth: authStub, DS: ds, SessionCache: cache}

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":false}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer managed-key")
		h.ChatCompletions(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}

	if ds.createCalls != 1 {
		t.Fatalf("CreateSession calls=%d want=1", ds.createCalls)
	}
	if got := strings.Join(ds.callSessions, ","); got != "session-a,session-b" {
		t.Fatalf("completion sessions=%s", got)
	}
}

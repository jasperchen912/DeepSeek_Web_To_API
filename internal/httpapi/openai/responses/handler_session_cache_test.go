package responses

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

type responsesSessionCacheAuthStub struct {
	a *auth.RequestAuth
}

func (s responsesSessionCacheAuthStub) Determine(_ *http.Request) (*auth.RequestAuth, error) {
	return cloneResponsesAuth(s.a), nil
}

func (s responsesSessionCacheAuthStub) DetermineCaller(_ *http.Request) (*auth.RequestAuth, error) {
	return cloneResponsesAuth(s.a), nil
}

func (s responsesSessionCacheAuthStub) DetermineWithSession(_ *http.Request, _ []byte) (*auth.RequestAuth, error) {
	return cloneResponsesAuth(s.a), nil
}

func (responsesSessionCacheAuthStub) Release(_ *auth.RequestAuth) {}

func cloneResponsesAuth(a *auth.RequestAuth) *auth.RequestAuth {
	if a == nil {
		return &auth.RequestAuth{DeepSeekToken: "token", CallerID: "caller", SessionKey: "session", TriedAccounts: map[string]bool{}}
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

type responsesSessionCacheDSStub struct {
	mu              sync.Mutex
	createSessions  []string
	createCalls     int
	callSessions    []string
	failOnceSession string
	failOnCall      int
	failedOnce      bool
}

func (s *responsesSessionCacheDSStub) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createCalls++
	idx := s.createCalls - 1
	if idx >= 0 && idx < len(s.createSessions) {
		return s.createSessions[idx], nil
	}
	return "session-responses", nil
}

func (s *responsesSessionCacheDSStub) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}

func (s *responsesSessionCacheDSStub) UploadFile(_ context.Context, _ *auth.RequestAuth, _ dsclient.UploadFileRequest, _ int) (*dsclient.UploadFileResult, error) {
	return &dsclient.UploadFileResult{ID: "file-id", Status: "uploaded"}, nil
}

func (s *responsesSessionCacheDSStub) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	sessionID, _ := payload["chat_session_id"].(string)
	s.mu.Lock()
	s.callSessions = append(s.callSessions, sessionID)
	callNumber := len(s.callSessions)
	if s.failOnceSession == sessionID && !s.failedOnce && (s.failOnCall == 0 || s.failOnCall == callNumber) {
		s.failedOnce = true
		s.mu.Unlock()
		return nil, &dsclient.RequestFailure{Kind: dsclient.FailureUpstreamStatus, StatusCode: http.StatusNotFound, Message: "chat session not found"}
	}
	s.mu.Unlock()
	return makeResponsesHistorySSEHTTPResponse(
		`data: {"p":"response/content","v":"hello"}`,
		`data: [DONE]`,
	), nil
}

func (s *responsesSessionCacheDSStub) DeleteSessionForToken(_ context.Context, _ string, _ string) (*dsclient.DeleteSessionResult, error) {
	return &dsclient.DeleteSessionResult{Success: true}, nil
}

func (s *responsesSessionCacheDSStub) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func TestResponsesReusesCachedSession(t *testing.T) {
	cache := sessioncache.New(sessioncache.Options{TTL: time.Hour, MaxEntries: 10})
	ds := &responsesSessionCacheDSStub{createSessions: []string{"session-1"}}
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth: responsesSessionCacheAuthStub{a: &auth.RequestAuth{
			UseConfigToken: false,
			DeepSeekToken:  "direct-token",
			CallerID:       "caller",
			SessionKey:     "session",
			TriedAccounts:  map[string]bool{},
		}},
		DS:           ds,
		SessionCache: cache,
	}

	body := `{"model":"deepseek-v4-flash","input":"hi","stream":false}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer direct-token")
		h.Responses(rec, req)
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
}

func TestResponsesCachedSessionInvalidatesAndRetries(t *testing.T) {
	cache := sessioncache.New(sessioncache.Options{TTL: time.Hour, MaxEntries: 10})
	ds := &responsesSessionCacheDSStub{createSessions: []string{"session-1", "session-2"}, failOnceSession: "session-1", failOnCall: 2}
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth: responsesSessionCacheAuthStub{a: &auth.RequestAuth{
			UseConfigToken: false,
			DeepSeekToken:  "direct-token",
			CallerID:       "caller",
			SessionKey:     "session",
			TriedAccounts:  map[string]bool{},
		}},
		DS:           ds,
		SessionCache: cache,
	}

	body := `{"model":"deepseek-v4-flash","input":"hi","stream":false}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer direct-token")
		h.Responses(rec, req)
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

func TestResponsesNewSessionInvalidatesAndRetries(t *testing.T) {
	cache := sessioncache.New(sessioncache.Options{TTL: time.Hour, MaxEntries: 10})
	ds := &responsesSessionCacheDSStub{createSessions: []string{"session-1", "session-2"}, failOnceSession: "session-1", failOnCall: 1}
	h := &Handler{
		Store: responsesHistoryConfigStub{},
		Auth: responsesSessionCacheAuthStub{a: &auth.RequestAuth{
			UseConfigToken: false,
			DeepSeekToken:  "direct-token",
			CallerID:       "caller",
			SessionKey:     "session",
			TriedAccounts:  map[string]bool{},
		}},
		DS:           ds,
		SessionCache: cache,
	}

	body := `{"model":"deepseek-v4-flash","input":"hi","stream":false}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer direct-token")
	h.Responses(rec, req)
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

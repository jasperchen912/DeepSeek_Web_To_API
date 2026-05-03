package client

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
	powpkg "DeepSeek_Web_To_API/pow"
)

func TestPrepareSessionAndPowStartsIndependentRequestsConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	doer := encodedBodyDoerFunc(func(req *http.Request) (*http.Response, error) {
		started <- req.URL.String()
		<-release
		return jsonResponse(req, optimisticPrepareResponseBody(req.URL.String())), nil
	})
	client := &Client{
		regular:    doer,
		maxRetries: 3,
	}

	type result struct {
		sessionID  string
		powHeader  string
		sessionErr error
		powErr     error
	}
	done := make(chan result, 1)
	go func() {
		sessionID, powHeader, sessionErr, powErr := client.PrepareSessionAndPow(context.Background(), &auth.RequestAuth{DeepSeekToken: "token"}, 3)
		done <- result{sessionID: sessionID, powHeader: powHeader, sessionErr: sessionErr, powErr: powErr}
	}()

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case url := <-started:
			seen[url] = true
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for both optimistic setup requests to start")
		}
	}
	if !seen[dsprotocol.DeepSeekCreateSessionURL] {
		t.Fatalf("create session request was not started concurrently: %#v", seen)
	}
	if !seen[dsprotocol.DeepSeekCreatePowURL] {
		t.Fatalf("pow request was not started concurrently: %#v", seen)
	}

	close(release)
	select {
	case got := <-done:
		if got.sessionErr != nil || got.powErr != nil {
			t.Fatalf("PrepareSessionAndPow returned errors: session=%v pow=%v", got.sessionErr, got.powErr)
		}
		if got.sessionID != "session-123" {
			t.Fatalf("sessionID=%q, want session-123", got.sessionID)
		}
		if strings.TrimSpace(got.powHeader) == "" {
			t.Fatal("expected non-empty pow header")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for PrepareSessionAndPow result")
	}
}

func optimisticPrepareResponseBody(url string) string {
	switch url {
	case dsprotocol.DeepSeekCreateSessionURL:
		return `{"code":0,"data":{"biz_code":0,"biz_data":{"id":"session-123"}}}`
	case dsprotocol.DeepSeekCreatePowURL:
		return optimisticPowChallengeResponse()
	default:
		return `{}`
	}
}

func optimisticPowChallengeResponse() string {
	const salt = "testsalt"
	const expireAt = int64(1700000000)
	hash := powpkg.DeepSeekHashV1([]byte(powpkg.BuildPrefix(salt, expireAt) + "0"))
	return `{"code":0,"data":{"biz_code":0,"biz_data":{"challenge":{"algorithm":"DeepSeekHashV1","challenge":"` +
		hex.EncodeToString(hash[:]) +
		`","salt":"` + salt + `","expire_at":1700000000,"difficulty":1,"signature":"sig","target_path":"` +
		dsprotocol.DeepSeekCompletionTargetPath +
		`"}}}}`
}

func jsonResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

package client

import (
	"context"
	"sync"

	"DeepSeek_Web_To_API/internal/auth"
)

// PrepareSessionAndPow starts the independent DeepSeek session and PoW setup
// requests in parallel for the common valid-token path. If either optimistic
// request fails, it falls back to the existing serial flow so token refresh and
// account switching keep their established behavior.
func (c *Client) PrepareSessionAndPow(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, string, error, error) {
	if maxAttempts <= 0 {
		maxAttempts = c.maxRetries
	}
	sessionID, pow, sessionErr, powErr := c.tryPrepareSessionAndPowOnce(ctx, a)
	if sessionErr == nil && powErr == nil {
		return sessionID, pow, nil, nil
	}

	remainingAttempts := maxAttempts - 1
	if remainingAttempts < 1 {
		remainingAttempts = 1
	}
	sessionID, sessionErr = c.CreateSession(ctx, a, remainingAttempts)
	if sessionErr != nil {
		return "", "", sessionErr, nil
	}
	pow, powErr = c.GetPow(ctx, a, remainingAttempts)
	if powErr != nil {
		return "", "", nil, powErr
	}
	return sessionID, pow, nil, nil
}

func (c *Client) tryPrepareSessionAndPowOnce(ctx context.Context, a *auth.RequestAuth) (string, string, error, error) {
	clients := c.requestClientsForAuth(ctx, a)

	var wg sync.WaitGroup
	var sessionID string
	var sessionErr error
	var pow string
	var powErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		sessionID, sessionErr = c.createCompletionRetrySession(ctx, a, clients)
	}()
	go func() {
		defer wg.Done()
		pow, powErr = c.getCompletionRetryPow(ctx, a, clients)
	}()
	wg.Wait()

	return sessionID, pow, sessionErr, powErr
}

package shared

import (
	"context"

	"DeepSeek_Web_To_API/internal/auth"
)

type SessionPowCaller interface {
	CreateSession(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
	GetPow(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (string, error)
}

type ConcurrentSessionPowCaller interface {
	PrepareSessionAndPow(ctx context.Context, a *auth.RequestAuth, maxAttempts int) (sessionID string, pow string, sessionErr error, powErr error)
}

func PrepareSessionAndPow(ctx context.Context, ds SessionPowCaller, a *auth.RequestAuth, maxAttempts int) (string, string, error, error) {
	if ds == nil {
		return "", "", nil, nil
	}
	if concurrent, ok := ds.(ConcurrentSessionPowCaller); ok {
		return concurrent.PrepareSessionAndPow(ctx, a, maxAttempts)
	}
	sessionID, sessionErr := ds.CreateSession(ctx, a, maxAttempts)
	if sessionErr != nil {
		return "", "", sessionErr, nil
	}
	pow, powErr := ds.GetPow(ctx, a, maxAttempts)
	if powErr != nil {
		return "", "", nil, powErr
	}
	return sessionID, pow, nil, nil
}

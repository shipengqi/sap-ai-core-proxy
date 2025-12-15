package contextx

import (
	"context"
)

type (
	// usernameKey Defines the context key for the username.
	usernameKey struct{}
	// userIDKey Defines the context key for the user ID.
	userIDKey struct{}
	// accessTokenKey Defines the context key for the token.
	accessTokenKey struct{}
	// requestIDKey Defines the context key for the request ID.
	requestIDKey struct{}
)

// WithUserID Stores the user ID in the context.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// UserID Extracts the user ID from the context.
func UserID(ctx context.Context) string {
	userID, _ := ctx.Value(userIDKey{}).(string)
	return userID
}

// WithUsername Stores the username in the context.
func WithUsername(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, usernameKey{}, username)
}

// Username User Extracts the username from the context.
func Username(ctx context.Context) string {
	username, _ := ctx.Value(usernameKey{}).(string)
	return username
}

// WithAccessToken Stores the access token in the context.
func WithAccessToken(ctx context.Context, accessToken string) context.Context {
	return context.WithValue(ctx, accessTokenKey{}, accessToken)
}

// AccessToken Extracts the access token from the context.
func AccessToken(ctx context.Context) string {
	accessToken, _ := ctx.Value(accessTokenKey{}).(string)
	return accessToken
}

// WithRequestID Stores the request ID in the context.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// RequestID Extracts the request ID from the context.
func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

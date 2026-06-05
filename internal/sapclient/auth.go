package sapclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type AuthManager struct {
	tokenURL     string
	clientID     string
	clientSecret string

	mu        sync.Mutex
	token     string
	expiresAt time.Time

	sf singleflight.Group
}

func NewAuthManager(tokenURL, clientID, clientSecret string) *AuthManager {
	return &AuthManager{
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

// GetToken returns a valid access token, refreshing if within 60s of expiry.
func (a *AuthManager) GetToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	if a.token != "" && time.Until(a.expiresAt) > 60*time.Second {
		tok := a.token
		a.mu.Unlock()
		slog.Debug("auth: token cache hit", "expires_in_s", int(time.Until(a.expiresAt).Seconds()))
		return tok, nil
	}
	a.mu.Unlock()

	v, err, _ := a.sf.Do("token", func() (interface{}, error) {
		return a.fetchToken(ctx)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func (a *AuthManager) fetchToken(ctx context.Context) (string, error) {
	slog.Info("auth: fetching new token", "url", a.tokenURL+"/oauth/token")
	form := url.Values{}
	form.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.tokenURL+"/oauth/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.SetBasicAuth(a.clientID, a.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("auth: token request failed", "err", err)
		return "", fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		slog.Error("auth: token endpoint error", "status", resp.StatusCode, "body", string(body))
		return "", fmt.Errorf("token endpoint %d: %s", resp.StatusCode, body)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}

	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 3600 * time.Second
	}

	a.mu.Lock()
	a.token = tr.AccessToken
	a.expiresAt = time.Now().Add(ttl)
	a.mu.Unlock()

	slog.Info("auth: token acquired", "expires_in_s", int(ttl.Seconds()))
	return tr.AccessToken, nil
}

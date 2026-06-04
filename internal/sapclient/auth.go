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
)

// AuthManager manages OAuth tokens for SAP AI Core.
type AuthManager struct {
	clientID      string
	clientSecret  string
	tokenURL      string
	baseURL       string
	resourceGroup string

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time

	sf sfGroup
}

// sfGroup is a minimal singleflight implementation to avoid importing golang.org/x/sync just for this.
// We use sync.Once reset pattern: store in-flight promise as a channel.
type sfGroup struct {
	mu       sync.Mutex
	inflight chan struct{}
	err      error
}

func NewAuthManager(clientID, clientSecret, tokenURL, baseURL, resourceGroup string) *AuthManager {
	return &AuthManager{
		clientID:      clientID,
		clientSecret:  clientSecret,
		tokenURL:      tokenURL,
		baseURL:       baseURL,
		resourceGroup: resourceGroup,
	}
}

// GetToken returns a valid access token, refreshing if within 60s of expiry.
// Concurrent callers share one in-flight fetch.
func (a *AuthManager) GetToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	if a.accessToken != "" && time.Until(a.expiresAt) > 60*time.Second {
		tok := a.accessToken
		a.mu.Unlock()
		return tok, nil
	}
	a.mu.Unlock()

	// Use channel-based singleflight
	a.sf.mu.Lock()
	if a.sf.inflight != nil {
		ch := a.sf.inflight
		a.sf.mu.Unlock()
		select {
		case <-ch:
			a.mu.Lock()
			tok, err := a.accessToken, a.sf.err
			a.mu.Unlock()
			if err != nil {
				return "", err
			}
			return tok, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	ch := make(chan struct{})
	a.sf.inflight = ch
	a.sf.mu.Unlock()

	tok, expiresIn, err := a.fetchToken(ctx)

	a.mu.Lock()
	if err == nil {
		a.accessToken = tok
		a.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	}
	a.mu.Unlock()

	a.sf.mu.Lock()
	a.sf.err = err
	a.sf.inflight = nil
	a.sf.mu.Unlock()
	close(ch)

	return tok, err
}

func (a *AuthManager) fetchToken(ctx context.Context) (string, int, error) {
	tokenURL := strings.TrimRight(a.tokenURL, "/") + "/oauth/token"
	slog.Debug("authenticating with SAP AI Core", "url", tokenURL)

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("response_type", "token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(a.clientID, a.clientSecret)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("token request returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", 0, fmt.Errorf("parse token response: %w", err)
	}
	if result.AccessToken == "" {
		return "", 0, fmt.Errorf("empty access_token in response")
	}

	slog.Info("authenticated with SAP AI Core")
	return result.AccessToken, result.ExpiresIn, nil
}

// BuildHeaders returns the auth + resource group headers for every SAP API request.
func (a *AuthManager) BuildHeaders(ctx context.Context) (map[string]string, error) {
	tok, err := a.GetToken(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"Authorization":    "Bearer " + tok,
		"AI-Resource-Group": a.resourceGroup,
		"Content-Type":     "application/json",
	}, nil
}

func (a *AuthManager) BaseURL() string { return a.baseURL }

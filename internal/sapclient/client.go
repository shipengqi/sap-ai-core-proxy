package sapclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Client is a signed HTTP client for SAP AI Core. It injects the Bearer token
// and AI-Resource-Group header on every request.
type Client struct {
	auth          *AuthManager
	resourceGroup string
	baseURL       string
	http          *http.Client
}

func NewClient(baseURL, resourceGroup string, auth *AuthManager) *Client {
	return &Client{
		auth:          auth,
		resourceGroup: resourceGroup,
		baseURL:       baseURL,
		http: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

func (c *Client) BaseURL() string { return c.baseURL }

// Do performs a signed request.
func (c *Client) Do(ctx context.Context, method, urlStr string, body io.Reader, extraHeaders map[string]string) (*http.Response, error) {
	return c.do(ctx, method, urlStr, body, extraHeaders, c.http)
}

// DoStreaming performs a signed request with no client timeout (for streaming responses).
func (c *Client) DoStreaming(ctx context.Context, method, urlStr string, body io.Reader, extraHeaders map[string]string) (*http.Response, error) {
	return c.do(ctx, method, urlStr, body, extraHeaders, &http.Client{})
}

func (c *Client) do(ctx context.Context, method, urlStr string, body io.Reader, extraHeaders map[string]string, httpClient *http.Client) (*http.Response, error) {
	token, err := c.auth.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}

	// Only buffer the body if non-nil; keep nil body truly nil for GET/DELETE.
	var reqBody io.Reader
	if body != nil {
		buf, err := io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, reqBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", c.resourceGroup)
	// Only set Content-Type for requests that carry a body.
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	slog.Info("upstream request", "method", method, "url", urlStr)
	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Error("upstream request failed", "method", method, "url", urlStr, "err", err)
		return nil, err
	}
	slog.Info("upstream response", "method", method, "url", urlStr, "status", resp.StatusCode, "latency_ms", time.Since(start).Milliseconds())
	return resp, nil
}

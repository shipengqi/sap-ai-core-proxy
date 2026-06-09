package sapclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	maxTransientRetries = 2
	retryBaseDelay      = 100 * time.Millisecond
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
	// Only buffer the body if non-nil; keep nil body truly nil for GET/DELETE.
	var buf []byte
	if body != nil {
		var err error
		buf, err = io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
	}

	for authAttempt := 0; authAttempt < 2; authAttempt++ {
		token, err := c.auth.GetToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("get token: %w", err)
		}

		resp, err := c.doOnce(ctx, method, urlStr, buf, extraHeaders, token, httpClient)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && authAttempt == 0 {
			_ = resp.Body.Close()
			slog.Warn("upstream 401, invalidating token and retrying", "method", method, "url", urlStr)
			c.auth.InvalidateToken()
			continue
		}
		return resp, nil
	}
	// unreachable
	return nil, nil
}

// doOnce executes a single signed HTTP request with transient-network retries.
func (c *Client) doOnce(ctx context.Context, method, urlStr string, buf []byte, extraHeaders map[string]string, token string, httpClient *http.Client) (*http.Response, error) {
	slog.Info("upstream request", "method", method, "url", urlStr)
	start := time.Now()

	var (
		resp *http.Response
		err  error
	)
	for attempt := 0; attempt <= maxTransientRetries; attempt++ {
		if attempt > 0 {
			delay := retryBaseDelay * time.Duration(attempt)
			slog.Warn("retrying transient network error", "method", method, "url", urlStr,
				"attempt", attempt, "delay", delay, "err", err)
			time.Sleep(delay)
		}

		var reqBody io.Reader
		if buf != nil {
			reqBody = bytes.NewReader(buf)
		}

		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, method, urlStr, reqBody)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("AI-Resource-Group", c.resourceGroup)
		if reqBody != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}

		resp, err = httpClient.Do(req)
		if err == nil || !isTransientNetworkErr(err) {
			break
		}
	}

	if err != nil {
		slog.Error("upstream request failed", "method", method, "url", urlStr, "err", err)
		return nil, err
	}
	elapsed := time.Since(start).Milliseconds()
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		preview := string(bodyBytes)
		if len(preview) > 500 {
			preview = preview[:500]
		}
		slog.Warn("upstream error response", "method", method, "url", urlStr,
			"status", resp.StatusCode, "latency_ms", elapsed, "body", preview)
	} else {
		slog.Info("upstream response", "method", method, "url", urlStr,
			"status", resp.StatusCode, "latency_ms", elapsed)
	}
	return resp, nil
}

// isTransientNetworkErr reports whether err is a transient network failure worth retrying.
// Only matches DNS-layer errors to avoid false positives from connection-level permission errors.
func isTransientNetworkErr(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTemporary || dnsErr.IsTimeout {
			return true
		}
		// EACCES/EPERM on the DNS UDP socket (e.g. "write: permission denied").
		// IsTemporary is unreliable for EACCES in Go 1.18+, so check the string
		// but only within DNSError.Err — not the broader OpError — to avoid
		// matching connection-level "permission denied" from a firewall.
		e := dnsErr.Err
		return strings.Contains(e, "permission denied") ||
			strings.Contains(e, "operation not permitted")
	}
	return false
}

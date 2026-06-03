package sapclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	inferenceTimeout = 30 * time.Second
	apiTimeout       = 10 * time.Second
)

// SapClient wraps net/http to call SAP AI Core endpoints with auth headers.
type SapClient struct {
	auth   *AuthManager
	client *http.Client
}

// cancelOnClose calls cancel when the body is closed, so the context outlives Do() for body reads.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	defer c.cancel()
	return c.ReadCloser.Close()
}

func NewSapClient(auth *AuthManager) *SapClient {
	return &SapClient{
		auth:   auth,
		client: &http.Client{},
	}
}

func (c *SapClient) Post(ctx context.Context, path string, body any) (*http.Response, error) {
	return c.do(ctx, http.MethodPost, path, body, inferenceTimeout, false)
}

// PostStream returns the streaming response. Returns *SapAPIError for 4xx upstream responses.
func (c *SapClient) PostStream(ctx context.Context, path string, body any) (*http.Response, error) {
	resp, err := c.do(ctx, http.MethodPost, path, body, inferenceTimeout, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		msg := parseErrorMessage(raw)
		return nil, NewSapAPIError(resp.StatusCode, msg)
	}
	return resp, nil
}

func (c *SapClient) Get(ctx context.Context, path string) (*http.Response, error) {
	return c.do(ctx, http.MethodGet, path, nil, apiTimeout, false)
}

func (c *SapClient) Delete(ctx context.Context, path string) (*http.Response, error) {
	return c.do(ctx, http.MethodDelete, path, nil, apiTimeout, false)
}

// PostForm sends a multipart/form-data body. contentType must include the boundary.
func (c *SapClient) PostForm(ctx context.Context, path string, body io.Reader, contentType string) (*http.Response, error) {
	headers, err := c.auth.BuildHeaders(ctx)
	if err != nil {
		return nil, err
	}
	// Override content-type with the multipart boundary from the caller
	headers["Content-Type"] = contentType

	url := c.auth.BaseURL() + path
	tctx, cancel := context.WithTimeout(ctx, inferenceTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(tctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.client.Do(req)
}

func (c *SapClient) do(ctx context.Context, method, path string, body any, timeout time.Duration, stream bool) (*http.Response, error) {
	headers, err := c.auth.BuildHeaders(ctx)
	if err != nil {
		return nil, err
	}

	url := c.auth.BaseURL() + path
	tctx, cancel := context.WithTimeout(ctx, timeout)

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(tctx, method, url, bodyReader)
	if err != nil {
		cancel()
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	// Wrap body so cancel() fires on Close(), not when do() returns.
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// parseErrorMessage tries to extract a human-readable message from SAP error JSON.
// Falls back to the raw body string.
func parseErrorMessage(body []byte) string {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		s := string(body)
		if s == "" {
			return "unknown error"
		}
		return s
	}
	// Try: errors.message, error.message, message
	if errs, ok := data["errors"].(map[string]any); ok {
		if msg, ok := errs["message"].(string); ok && msg != "" {
			return msg
		}
	}
	if errObj, ok := data["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok && msg != "" {
			return msg
		}
	}
	if msg, ok := data["message"].(string); ok && msg != "" {
		return msg
	}
	return string(body)
}

// Package proxy holds HTTP behavior shared by the provider handlers.
package proxy

import (
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/usage"
)

// CheckStream preserves failures received before any streaming bytes are sent.
func CheckStream(c *gin.Context, upstream *http.Response) bool {
	usage.MarkStream(c)
	usage.MarkUpstreamStatus(c, upstream.StatusCode)
	CopyHeaders(c, upstream)
	if upstream.StatusCode >= 200 && upstream.StatusCode < 300 {
		return true
	}
	defer func() { _ = upstream.Body.Close() }()
	body, err := io.ReadAll(upstream.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "upstream_error", "message": "read upstream error: " + err.Error()}})
		return false
	}
	contentType := upstream.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(upstream.StatusCode, contentType, body)
	return false
}

func CopyHeaders(c *gin.Context, upstream *http.Response) {
	// A parameter retry or failover replaces metadata from the previous attempt.
	for name := range c.Writer.Header() {
		if forwardHeader(name) {
			c.Writer.Header().Del(name)
		}
	}
	for name, values := range upstream.Header {
		// Preserve provider request IDs and rate limits without forwarding hop-by-hop headers.
		if forwardHeader(name) {
			for _, value := range values {
				c.Writer.Header().Add(name, value)
			}
		}
	}
}

func forwardHeader(name string) bool {
	lower := strings.ToLower(name)
	return lower == "retry-after" || lower == "request-id" || lower == "x-request-id" || lower == "anthropic-request-id" ||
		strings.HasPrefix(lower, "ratelimit") || strings.HasPrefix(lower, "x-ratelimit") || strings.HasPrefix(lower, "anthropic-ratelimit-")
}

package gin

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// NoCache is a middleware function that appends headers
// to prevent the client from caching the HTTP response.
func NoCache(c *gin.Context) {
	c.Header("Cache-Control", "no-cache, no-store, max-age=0, must-revalidate")
	c.Header("Expires", "Thu, 01 Jan 1970 00:00:00 GMT")
	c.Header("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
	c.Next()
}

// Cors is a Gin middleware used to handle CORS requests.
func Cors(c *gin.Context) {
	if c.Request.Method == http.MethodOptions {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "authorization, origin, content-type, accept")
		c.Header("Allow", "HEAD, GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Content-Type", "application/json")
		c.AbortWithStatus(http.StatusOK)
		return
	}
	c.Next()
}

// Secure is a Gin middleware used to add security-related HTTP headers.
func Secure(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")

	// Disables MIME sniffing and forces browser to use the type given in Content-Type.
	c.Header("X-Content-Type-Options", "nosniff")

	// Enables cross-site scripting filtering.
	// "1" Enables XSS filtering
	// "mode=block" Enables XSS filtering. Rather than sanitizing the page,
	// the browser will prevent rendering of the page if an attack is detected.
	c.Header("X-XSS-Protection", "1; mode=block")

	// Indicates whether a browser should be allowed to render a page in a <frame>, <iframe>, <embed> or <object>.
	// c.Header("X-Frame-Options", "DENY")

	// Also consider adding Content-Security-Policy headers
	// c.Header("Content-Security-Policy", "script-src 'self' https://cdnjs.cloudflare.com")

	if c.Request.TLS != nil {
		// Force communication using HTTPS instead of HTTP.
		// "max-age=<expire-time>" The time, in seconds,
		// that the browser should remember that a site is only to be accessed using HTTPS.
		c.Header("Strict-Transport-Security", "max-age=31536000")
	}
	c.Next()
}

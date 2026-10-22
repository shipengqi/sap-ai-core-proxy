package testutil

import (
	"net/http/httptest"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/internal/config"
	"github.com/shipengqi/sap-ai-core-proxy/internal/router"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// NewTestProxy starts the real Gin proxy pointed at mockURL.
// If mockURL is empty, it reads SAP_AI_CORE_* env vars (real SAP mode).
func NewTestProxy(mockURL string) (*httptest.Server, error) {
	return NewTestProxyWithAnthropicAutoCache(mockURL, false)
}

// NewTestProxyWithAnthropicAutoCache starts a proxy with explicit Claude cache-marker configuration.
func NewTestProxyWithAnthropicAutoCache(mockURL string, anthropicAutoCache bool) (*httptest.Server, error) {
	var cfg *config.Config

	if mockURL != "" {
		cfg = &config.Config{
			SAPAICore: config.SAPAICore{
				BaseURL:       mockURL,
				TokenURL:      mockURL,
				ClientID:      "test-client",
				ClientSecret:  "test-secret",
				ResourceGroup: "test-group",
			},
			Server: config.Server{Port: 0, LogLevel: "error", Metrics: true, AnthropicAutoCache: anthropicAutoCache},
		}
	} else {
		var err error
		cfg, err = config.Load()
		if err != nil {
			return nil, err
		}
		cfg.Server.AnthropicAutoCache = anthropicAutoCache
	}

	auth := sapclient.NewAuthManager(cfg.SAPAICore.TokenURL, cfg.SAPAICore.ClientID, cfg.SAPAICore.ClientSecret)
	client := sapclient.NewClient(cfg.SAPAICore.BaseURL, cfg.SAPAICore.ResourceGroup, auth)
	deployments := sapclient.NewDeploymentManager(client)

	// The usage endpoints are always enabled in tests so the observation path is
	// exercised by the integration suite.
	r := router.New(client, deployments, false, true, cfg.Server.AnthropicAutoCache)
	return httptest.NewServer(r), nil
}

// RealCreds returns the SAP AI Core credentials from environment variables.
// ok is true only when all required vars are set.
func RealCreds() (baseURL, tokenURL, clientID, clientSecret string, ok bool) {
	baseURL = os.Getenv("SAP_AI_CORE_BASE_URL")
	tokenURL = os.Getenv("SAP_AI_CORE_TOKEN_URL")
	clientID = os.Getenv("SAP_AI_CORE_CLIENT_ID")
	clientSecret = os.Getenv("SAP_AI_CORE_CLIENT_SECRET")
	ok = baseURL != "" && tokenURL != "" && clientID != "" && clientSecret != ""
	return
}

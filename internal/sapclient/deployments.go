package sapclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
)

const deploymentCacheTTL = 12 * time.Hour

// Deployment represents a single SAP AI Core LM deployment.
type Deployment struct {
	ID              string            `json:"id"`
	Status          string            `json:"status"`
	ModelName       string            `json:"modelName"` // flat field (some SAP versions)
	ScenarioID      string            `json:"scenarioId"`
	DeployedURL     string            `json:"deploymentUrl"`
	ResourceGroupID string            `json:"resourceGroupId"`
	Details         deploymentDetails `json:"details"`
}

type deploymentDetails struct {
	Resources struct {
		BackendDetails  *backendModel `json:"backendDetails"`
		BackendDetails2 *backendModel `json:"backend_details"` // snake_case variant
	} `json:"resources"`
}

type backendModel struct {
	Model struct {
		Name string `json:"name"`
	} `json:"model"`
}

// EffectiveModelName returns the model name from whichever field is populated.
func (d *Deployment) EffectiveModelName() string {
	if d.ModelName != "" {
		return d.ModelName
	}
	if d.Details.Resources.BackendDetails != nil && d.Details.Resources.BackendDetails.Model.Name != "" {
		return d.Details.Resources.BackendDetails.Model.Name
	}
	if d.Details.Resources.BackendDetails2 != nil && d.Details.Resources.BackendDetails2.Model.Name != "" {
		return d.Details.Resources.BackendDetails2.Model.Name
	}
	return ""
}

type deploymentsResponse struct {
	Resources []Deployment `json:"resources"`
}

// blacklistTTL is how long a deployment stays blacklisted after a fatal SAP error.
const blacklistTTL = time.Hour

// DeploymentManager fetches and caches SAP AI Core deployments with a 12-hour TTL.
// It also maintains a short-lived blacklist for deployments that return 404/Gone,
// so they are skipped on subsequent requests and excluded from the models list.
type DeploymentManager struct {
	client    *Client
	mu        sync.RWMutex
	cached    []Deployment
	fetchedAt time.Time

	blMu      sync.Mutex
	blacklist map[string]time.Time // deploymentURL → blacklisted-until
}

func NewDeploymentManager(client *Client) *DeploymentManager {
	return &DeploymentManager{
		client:    client,
		blacklist: make(map[string]time.Time),
	}
}

// MarkUnhealthy blacklists a deployment URL for blacklistTTL.
// Call this when SAP returns 404 or "Gone" for an inference request.
func (d *DeploymentManager) MarkUnhealthy(deployedURL string) {
	d.blMu.Lock()
	d.blacklist[deployedURL] = time.Now().Add(blacklistTTL)
	d.blMu.Unlock()
	slog.Warn("deployment blacklisted", "url", deployedURL, "ttl", blacklistTTL)
}

func (d *DeploymentManager) isBlacklisted(deployedURL string) bool {
	d.blMu.Lock()
	until, ok := d.blacklist[deployedURL]
	if ok && time.Now().After(until) {
		delete(d.blacklist, deployedURL)
		ok = false
	}
	d.blMu.Unlock()
	return ok
}

// GetDeploymentURL resolves the upstream URL for a given model name.
// Matching order: exact → fuzzy substring → family prefix.
// Logs a warning when falling back to a non-exact match.
func (d *DeploymentManager) GetDeploymentURL(ctx context.Context, modelName string) (string, error) {
	dep, err := d.findDeployment(ctx, modelName)
	if err != nil {
		return "", err
	}
	return dep.DeployedURL, nil
}

// GetDeployment returns the full Deployment (including ResourceGroupID) for a model.
func (d *DeploymentManager) GetDeployment(ctx context.Context, modelName string) (*Deployment, error) {
	return d.findDeployment(ctx, modelName)
}

func (d *DeploymentManager) findDeployment(ctx context.Context, modelName string) (*Deployment, error) {
	deployments, err := d.list(ctx)
	if err != nil {
		return nil, err
	}

	// Build a deduplicated list of candidate names to search:
	// 1. the catalogue-aliased name (e.g. "anthropic--claude-4.7-opus")
	// 2. the original user-facing name (e.g. "claude-opus-latest")
	// This lets the alias hint SAP's internal name while the original provides
	// a fuzzy/family fallback when the SAP environment uses a different name.
	aliased := catalogue.Normalize(modelName)
	candidates := []string{aliased}
	if aliased != modelName {
		candidates = append(candidates, modelName)
	}

	for _, cand := range candidates {
		target := strings.ToLower(strings.TrimSpace(cand))

		// 1. Exact match.
		for i, dep := range deployments {
			if d.isBlacklisted(dep.DeployedURL) {
				continue
			}
			if strings.ToLower(dep.EffectiveModelName()) == target {
				return &deployments[i], nil
			}
		}

		// 2. Fuzzy substring match.
		for i, dep := range deployments {
			if d.isBlacklisted(dep.DeployedURL) {
				continue
			}
			dn := normalizeDepName(dep.EffectiveModelName())
			if dn == "" {
				continue
			}
			if strings.Contains(target, dn) || strings.Contains(dn, target) {
				slog.Warn("deployment fuzzy match", "requested", modelName, "candidate", cand, "matched", dep.EffectiveModelName())
				return &deployments[i], nil
			}
		}

		// 3. Family prefix fallback (strips version suffix).
		family := extractFamily(target)
		if family != target {
			for i, dep := range deployments {
				if d.isBlacklisted(dep.DeployedURL) {
					continue
				}
				dn := normalizeDepName(dep.EffectiveModelName())
				if strings.Contains(dn, family) {
					slog.Warn("deployment family fallback", "requested", modelName, "candidate", cand, "family", family, "matched", dep.EffectiveModelName())
					return &deployments[i], nil
				}
			}
		}
	}

	return nil, fmt.Errorf("no running deployment found for model %q", modelName)
}

// FindAndCall resolves a deployment for modelName, calls fn(dep), and if SAP
// returns a retryable error (404 / Gone) marks the deployment unhealthy and
// tries the next matching deployment. At most maxRetries attempts are made.
//
// fn must return (statusCode, body, error). A statusCode of 404 or a body
// containing "Gone" triggers a retry with the next available deployment.
func (d *DeploymentManager) FindAndCall(
	ctx context.Context,
	modelName string,
	maxRetries int,
	fn func(dep *Deployment) (int, []byte, error),
) (int, []byte, error) {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		dep, err := d.findDeployment(ctx, modelName)
		if err != nil {
			return 0, nil, err
		}

		status, body, err := fn(dep)
		if err != nil {
			return status, body, err
		}

		if isRetryableStatus(status, body) {
			slog.Warn("deployment returned retryable error, blacklisting",
				"url", dep.DeployedURL, "status", status, "attempt", attempt+1)
			d.MarkUnhealthy(dep.DeployedURL)
			continue
		}

		return status, body, nil
	}
	return 0, nil, fmt.Errorf("all deployments for model %q returned errors", modelName)
}

// isRetryableStatus returns true for SAP errors that indicate the deployment
// is not usable: 404 (wrong resource group / not found) or "Gone" (retired).
func isRetryableStatus(status int, body []byte) bool {
	if status == 404 {
		return true
	}
	// SAP returns 410 Gone or 200 with {"error":"Gone"} for retired deployments.
	if status == 410 {
		return true
	}
	return strings.Contains(string(body), `"Gone"`)
}
func (d *DeploymentManager) GetOrchestrationURL(ctx context.Context) (string, error) {
	dep, err := d.GetOrchestrationDeployment(ctx)
	if err != nil {
		return "", err
	}
	return dep.DeployedURL, nil
}

// GetOrchestrationDeployment returns the full Deployment for the Orchestration service.
func (d *DeploymentManager) GetOrchestrationDeployment(ctx context.Context) (*Deployment, error) {
	deployments, err := d.list(ctx)
	if err != nil {
		return nil, err
	}
	for i, dep := range deployments {
		if dep.ScenarioID == "orchestration" {
			return &deployments[i], nil
		}
	}
	for i, dep := range deployments {
		if strings.Contains(strings.ToLower(dep.EffectiveModelName()), "orchestration") {
			return &deployments[i], nil
		}
	}
	return nil, fmt.Errorf("no orchestration deployment found")
}

// ListAll returns all cached deployments, excluding blacklisted ones.
func (d *DeploymentManager) ListAll(ctx context.Context) ([]Deployment, error) {
	all, err := d.list(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, dep := range all {
		if !d.isBlacklisted(dep.DeployedURL) {
			out = append(out, dep)
		}
	}
	return out, nil
}

func (d *DeploymentManager) list(ctx context.Context) ([]Deployment, error) {
	d.mu.RLock()
	if !d.fetchedAt.IsZero() && time.Since(d.fetchedAt) < deploymentCacheTTL {
		deps := d.cached
		d.mu.RUnlock()
		return deps, nil
	}
	d.mu.RUnlock()

	d.mu.Lock()
	defer d.mu.Unlock()

	// Double-check after acquiring write lock.
	if !d.fetchedAt.IsZero() && time.Since(d.fetchedAt) < deploymentCacheTTL {
		return d.cached, nil
	}

	deps, err := d.fetch(ctx)
	if err != nil {
		return nil, err
	}
	d.cached = deps
	d.fetchedAt = time.Now()
	return deps, nil
}

func (d *DeploymentManager) fetch(ctx context.Context) ([]Deployment, error) {
	resp, err := d.client.Do(ctx, http.MethodGet,
		d.client.BaseURL()+"/v2/lm/deployments",
		nil, nil)
	// AI-Resource-Group header is injected by client.Do from the configured resource group.
	if err != nil {
		return nil, fmt.Errorf("fetch deployments: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deployments endpoint %d: %s", resp.StatusCode, body)
	}

	var dr deploymentsResponse
	if err := json.Unmarshal(body, &dr); err != nil {
		return nil, fmt.Errorf("parse deployments: %w", err)
	}

	var running []Deployment
	for _, dep := range dr.Resources {
		if dep.Status == "RUNNING" && dep.DeployedURL != "" {
			running = append(running, dep)
			slog.Info("deployment loaded", "id", dep.ID, "model", dep.EffectiveModelName(), "url", dep.DeployedURL)
		}
	}
	slog.Info("deployments refreshed", "count", len(running))
	return running, nil
}

// normalizeDepName strips the SAP vendor prefix (e.g. "anthropic--") so that
// "anthropic--claude-3.5-sonnet" compares cleanly against "claude-3.5-sonnet".
func normalizeDepName(name string) string {
	lower := strings.ToLower(name)
	if idx := strings.Index(lower, "--"); idx >= 0 {
		lower = lower[idx+2:]
	}
	return lower
}

// extractFamily strips a trailing version segment from a model name.
// Examples: "claude-opus-4.8" → "claude-opus", "gpt-4.1-mini" → "gpt-4"
func extractFamily(model string) string {
	parts := strings.Split(model, "-")
	for i := len(parts) - 1; i > 0; i-- {
		last := parts[i]
		// If the last segment looks like a version (starts with digit or is "latest"/"preview"),
		// strip it.
		if last == "latest" || last == "preview" || (len(last) > 0 && last[0] >= '0' && last[0] <= '9') {
			return strings.Join(parts[:i], "-")
		}
	}
	return model
}

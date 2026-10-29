package sapclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
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
// It also maintains a short-lived blacklist for explicitly retired deployments,
// so they are skipped on subsequent requests and excluded from the models list.
type DeploymentManager struct {
	client              *Client
	mu                  sync.RWMutex
	cached              []Deployment
	fetchedAt           time.Time
	recoveryRefreshedAt time.Time

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
// Call this when SAP explicitly reports retirement or a missing deployment.
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
// Only exact names, explicit aliases and vendor-prefix normalization match.
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

	// 0. Bare "-latest" aliases (claude-opus-latest, claude-sonnet-latest,
	// claude-haiku-latest) are resolved against the live deployment list so
	// they always mean the newest running version of that family. Pinning
	// them in the alias table instead would freeze them at whatever version
	// was current when the table was last edited.
	if family, ok := catalogue.LatestFamily(modelName); ok {
		if dep := d.newestOfFamily(deployments, family); dep != nil {
			slog.Info("resolved -latest alias against deployments",
				"requested", modelName, "family", family, "matched", dep.EffectiveModelName())
			return dep, nil
		}
		slog.Warn("no deployment found for -latest family, falling back to alias table",
			"requested", modelName, "family", family)
	}

	// Build a deduplicated list of candidate names to search:
	// 1. the catalogue-aliased name (e.g. "anthropic--claude-4.8-opus")
	// 2. the original user-facing name (e.g. "claude-opus-4-8")
	// Both candidates refer to the same model under different naming conventions.
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

		// Vendor prefixes are naming differences, not permission to change models.
		for i, dep := range deployments {
			if d.isBlacklisted(dep.DeployedURL) {
				continue
			}
			dn := normalizeDepName(dep.EffectiveModelName())
			if dn == "" {
				continue
			}
			if normalizeDepName(target) == dn {
				return &deployments[i], nil
			}
		}
	}

	return nil, fmt.Errorf("no running deployment found for model %q", modelName)
}

// FindAndCall retries only explicit deployment retirement, on instances of the
// initially resolved model. maxRetries excludes the first attempt.
func (d *DeploymentManager) FindAndCall(
	ctx context.Context,
	modelName string,
	maxRetries int,
	fn func(dep *Deployment) (int, []byte, error),
) (int, []byte, error) {
	resolved := modelName
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		dep, err := d.findDeployment(ctx, resolved)
		if err != nil && attempt > 0 {
			dep, err = d.findReplacement(ctx, resolved)
		}
		if err != nil {
			return 0, nil, err
		}
		resolved = dep.EffectiveModelName()

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

// CallStreaming can fail over before any downstream bytes are written. It
// never retries an in-progress generation or changes the resolved model.
func (d *DeploymentManager) CallStreaming(ctx context.Context, initial *Deployment, maxRetries int, call func(*Deployment) (*http.Response, error)) (*http.Response, *Deployment, error) {
	dep := initial
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, dep, err
		}
		resp, err := call(dep)
		if err != nil {
			return nil, dep, err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, dep, nil
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
		_ = resp.Body.Close()
		if err != nil {
			return nil, dep, fmt.Errorf("read upstream error: %w", err)
		}
		if len(body) > 8<<20 {
			return nil, dep, fmt.Errorf("upstream error body exceeds 8 MiB")
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if !isRetryableStatus(resp.StatusCode, body) {
			return resp, dep, nil
		}
		d.MarkUnhealthy(dep.DeployedURL)
		if attempt >= maxRetries {
			return resp, dep, nil
		}
		next, err := d.findReplacement(ctx, initial.EffectiveModelName())
		if err != nil {
			return resp, dep, nil
		}
		_ = resp.Body.Close()
		dep = next
	}
}

// Refresh stale discovery after the known instances retire, at most once per
// minute across requests. The replacement still has to match the pinned model.
func (d *DeploymentManager) findReplacement(ctx context.Context, model string) (*Deployment, error) {
	dep, err := d.findDeployment(ctx, model)
	if err == nil || d.client == nil || ctx.Err() != nil {
		return dep, err
	}
	d.mu.Lock()
	if time.Since(d.recoveryRefreshedAt) < time.Minute {
		d.mu.Unlock()
		return nil, err
	}
	d.recoveryRefreshedAt = time.Now()
	d.fetchedAt = time.Time{}
	d.mu.Unlock()
	return d.findDeployment(ctx, model)
}

// isRetryableStatus excludes generic endpoint errors and normal completion text.
func isRetryableStatus(status int, body []byte) bool {
	// A missing endpoint is not evidence that the whole deployment is retired.
	// SAP returns 410 Gone or 200 with {"error":"Gone"} for retired deployments.
	if status == 410 {
		return true
	}
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Error) == 0 {
		return false
	}
	var message string
	if json.Unmarshal(envelope.Error, &message) == nil {
		return message == "Gone"
	}
	var detail struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(envelope.Error, &detail) != nil {
		return false
	}
	return detail.Code == "Gone" || detail.Message == "Gone" || status == 404 && detail.Code == "deployment_not_found"
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
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("read deployments: %w", readErr)
	}
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
			slog.Debug("deployment loaded", "id", dep.ID, "model", dep.EffectiveModelName(), "url", dep.DeployedURL)
		}
	}
	slog.Info("deployments refreshed", "count", len(running))
	return running, nil
}

// claudeVersionPattern matches a SAP Claude deployment name once the vendor
// prefix has been stripped, e.g. "claude-4.8-opus", "claude-3.5-sonnet",
// "claude-3-opus". Capture 1 is the version, capture 2 the family.
var claudeVersionPattern = regexp.MustCompile(`^claude-(\d+(?:\.\d+)?)-(opus|sonnet|haiku)$`)

// newestOfFamily returns the running deployment with the highest version
// number among those in the given Claude family, or nil if the family has no
// running deployment. Blacklisted deployments are skipped.
func (d *DeploymentManager) newestOfFamily(deployments []Deployment, family string) *Deployment {
	var best *Deployment
	var bestVer []int

	for i := range deployments {
		dep := &deployments[i]
		if d.isBlacklisted(dep.DeployedURL) {
			continue
		}
		m := claudeVersionPattern.FindStringSubmatch(normalizeDepName(dep.EffectiveModelName()))
		if m == nil || m[2] != family {
			continue
		}
		ver := parseVersion(m[1])
		if best == nil || compareVersion(ver, bestVer) > 0 {
			best, bestVer = dep, ver
		}
	}
	return best
}

// parseVersion turns "4.8" into [4 8] and "3" into [3].
func parseVersion(v string) []int {
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, _ := strconv.Atoi(p)
		out[i] = n
	}
	return out
}

// compareVersion compares two dotted version slices, treating missing segments
// as zero so that [4] < [4 8].
func compareVersion(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var av, bv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	return 0
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

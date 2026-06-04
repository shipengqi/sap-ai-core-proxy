package sapclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Deployment represents a running SAP AI Core model deployment.
type Deployment struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	TargetStatus string `json:"targetStatus"`
	CreatedAt    string `json:"createdAt"`
	Details      struct {
		Resources struct {
			BackendDetails struct {
				Model struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"model"`
			} `json:"backend_details"`
		} `json:"resources"`
	} `json:"details"`
}

func (d *Deployment) ModelName() string {
	return d.Details.Resources.BackendDetails.Model.Name
}

type deploymentsResponse struct {
	Resources []Deployment `json:"resources"`
}

// DeploymentSummary is returned by Refresh for admin endpoints.
type DeploymentSummary struct {
	ID      string `json:"id"`
	SAPName string `json:"sapName"`
	Status  string `json:"status"`
}

// DeploymentManager fetches and caches the list of running deployments.
type DeploymentManager struct {
	client        *SapClient
	mu            sync.Mutex
	deployments   []Deployment
	lastFetchTime time.Time
	cacheDuration time.Duration

	sfMu     sync.Mutex
	sfCh     chan struct{}
	sfErr    error
}

func NewDeploymentManager(auth *AuthManager) *DeploymentManager {
	return &DeploymentManager{
		client:        NewSapClient(auth),
		cacheDuration: 60 * time.Second,
	}
}

// WarmUp pre-fetches deployments at startup. Non-blocking — logs warning on failure.
func (d *DeploymentManager) WarmUp(ctx context.Context) {
	if _, err := d.GetDeployments(ctx); err != nil {
		slog.Warn("failed to pre-fetch deployments at startup", "error", err)
	}
}

// GetDeployments returns cached deployments, fetching fresh if the cache is stale.
// Concurrent callers share one in-flight fetch (singleflight pattern).
func (d *DeploymentManager) GetDeployments(ctx context.Context) ([]Deployment, error) {
	d.mu.Lock()
	if len(d.deployments) > 0 && time.Since(d.lastFetchTime) < d.cacheDuration {
		deps := d.deployments
		d.mu.Unlock()
		return deps, nil
	}
	d.mu.Unlock()

	// Singleflight: only one fetch at a time
	d.sfMu.Lock()
	if d.sfCh != nil {
		ch := d.sfCh
		d.sfMu.Unlock()
		select {
		case <-ch:
			d.mu.Lock()
			deps := d.deployments
			err := d.sfErr
			d.mu.Unlock()
			return deps, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ch := make(chan struct{})
	d.sfCh = ch
	d.sfMu.Unlock()

	deps, err := d.fetchDeployments(ctx)

	d.mu.Lock()
	if err == nil {
		d.deployments = deps
		d.lastFetchTime = time.Now()
	}
	d.mu.Unlock()

	d.sfMu.Lock()
	d.sfErr = err
	d.sfCh = nil
	d.sfMu.Unlock()
	close(ch)

	return deps, err
}

func (d *DeploymentManager) fetchDeployments(ctx context.Context) ([]Deployment, error) {
	slog.Debug("fetching deployments from SAP AI Core")

	resp, err := d.client.Get(ctx, "/v2/lm/deployments?$top=10000&$skip=0")
	if err != nil {
		return nil, fmt.Errorf("fetch deployments: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("deployments API returned %d: %s", resp.StatusCode, body)
	}

	var result deploymentsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse deployments response: %w", err)
	}

	var running []Deployment
	for _, dep := range result.Resources {
		name := dep.ModelName()
		if name == "" {
			continue
		}
		if dep.Status == "RUNNING" || dep.TargetStatus == "RUNNING" {
			running = append(running, dep)
			slog.Debug("deployment found", "model", name, "version", dep.Details.Resources.BackendDetails.Model.Version, "id", dep.ID)
		}
	}

	slog.Info("deployments loaded", "count", len(running))
	return running, nil
}

// GetDeploymentID returns the deployment ID for the given SAP model name.
func (d *DeploymentManager) GetDeploymentID(ctx context.Context, modelName string) (string, error) {
	deps, err := d.GetDeployments(ctx)
	if err != nil {
		return "", err
	}
	for _, dep := range deps {
		if dep.ModelName() == modelName {
			return dep.ID, nil
		}
	}
	return "", fmt.Errorf("no running deployment found for model: %s", modelName)
}

// GetDeploymentIDFromChain tries each SAP name in order and returns the first with
// a running deployment. Returns the deployment ID and the resolved SAP name.
// This is used for -latest alias requests so they fall back gracefully when the
// newest model version has not been deployed yet.
func (d *DeploymentManager) GetDeploymentIDFromChain(ctx context.Context, chain []string) (string, string, error) {
	deps, err := d.GetDeployments(ctx)
	if err != nil {
		return "", "", err
	}
	depMap := make(map[string]string, len(deps))
	for _, dep := range deps {
		depMap[dep.ModelName()] = dep.ID
	}
	for _, name := range chain {
		if id, ok := depMap[name]; ok {
			return id, name, nil
		}
	}
	return "", "", fmt.Errorf("no running deployment found for any of: %v", chain)
}

// Refresh forces a cache invalidation and returns a summary of all deployments.
func (d *DeploymentManager) Refresh(ctx context.Context) ([]DeploymentSummary, error) {
	d.mu.Lock()
	d.lastFetchTime = time.Time{} // zero = stale
	d.deployments = nil
	d.mu.Unlock()

	deps, err := d.GetDeployments(ctx)
	if err != nil {
		return nil, err
	}
	summaries := make([]DeploymentSummary, len(deps))
	for i, dep := range deps {
		summaries[i] = DeploymentSummary{
			ID:      dep.ID,
			SAPName: dep.ModelName(),
			Status:  dep.Status,
		}
	}
	return summaries, nil
}

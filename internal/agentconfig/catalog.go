package agentconfig

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Model is one entry in the deliberately small curated catalog. ModelID is
// what gets sent upstream; it differs from ID only when a deployment name
// needs to be mapped to a friendlier OpenCode model ID.
type Model struct {
	ID      string
	Name    string
	ModelID string
	Context int
	Output  int
}

var catalog = map[Family][]Model{
	FamilyClaude: {
		{ID: "claude-sonnet-4-6", Name: "Sonnet 4.6", Context: 200000, Output: 128000},
		{ID: "claude-opus-4-8", Name: "Opus 4.8", Context: 1000000, Output: 128000},
		{ID: "claude-haiku-4-5", Name: "Haiku 4.5", Context: 200000, Output: 64000},
	},
	FamilyGPT: {
		{ID: "gpt-6.1-sol", Name: "GPT-6.1 Sol", Context: 1050000, Output: 128000},
		{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Context: 1050000, Output: 128000},
		{ID: "gpt-5.4", Name: "GPT-5.4", Context: 1050000, Output: 128000},
	},
}

// ParseModels parses "claude=id,id;gpt=id,id". Families are separated by ';'
// because commas separate model IDs inside a family. Unknown families are
// rejected.
func ParseModels(spec string) (map[Family][]string, error) {
	result := map[Family][]string{}
	for _, part := range strings.Split(spec, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, values, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --models entry %q (want family=id,id)", part)
		}
		family := Family(strings.TrimSpace(name))
		if family != FamilyClaude && family != FamilyGPT {
			return nil, fmt.Errorf("unknown model family %q (choose claude or gpt)", name)
		}
		for _, id := range strings.FieldsFunc(values, func(r rune) bool { return r == ',' || r == ':' || r == ' ' }) {
			if id = strings.TrimSpace(id); id != "" {
				result[family] = append(result[family], id)
			}
		}
		if len(result[family]) == 0 {
			return nil, fmt.Errorf("no models given for %s", family)
		}
	}
	return result, nil
}

// ModelsFor returns the curated list for a family, honouring --models
// overrides and the optional live deployment discovery result.
func ModelsFor(opts Options, family Family) []Model {
	var models []Model
	if ids := opts.Models[family]; len(ids) > 0 {
		for _, id := range ids {
			models = append(models, Model{ID: id, Name: id, Context: 200000, Output: 64000})
		}
	} else {
		models = append(models, catalog[family]...)
	}
	filtered := models[:0]
	for _, m := range models {
		if opts.allowed(m.ID) {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

func defaultModel(opts Options, family Family) string {
	models := ModelsFor(opts, family)
	if len(models) == 0 {
		return ""
	}
	return models[0].ID
}

// DiscoverModels is best-effort: it asks a running proxy which deployments are
// actually available. Failures are reported but never block configuration.
func DiscoverModels(baseURL string) (map[string]bool, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(baseURL, "/") + "/openai/v1/models")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("GET /openai/v1/models: %s", resp.Status)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode /openai/v1/models: %w", err)
	}
	available := map[string]bool{}
	for _, item := range payload.Data {
		available[item.ID] = true
	}
	return available, nil
}

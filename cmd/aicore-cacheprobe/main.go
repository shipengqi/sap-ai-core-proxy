// Command aicore-cacheprobe reports whether a deployment caches its prompt, and
// whether it accepts the explicit cache parameters.
//
// It exists because prompt caching is not one mechanism. Anthropic takes
// cache_control breakpoints. OpenAI caches automatically and, from GPT-5.6 on,
// also takes explicit breakpoints — but older OpenAI models reject those
// parameters with a 400. Guessing from a model name is unreliable, so the only
// trustworthy answer comes from asking the deployment itself.
//
// The probe sends a fixed ~2000 token prefix five times per model and reads the
// usage block back:
//
//	cold        no cache parameters; the miss baseline
//	warm        identical to cold; a non-zero cached_tokens here proves implicit
//	            caching works with no marker at all
//	explicit#1  prompt_cache_options + prompt_cache_breakpoint; a 200 means the
//	            deployment accepts them, a 400 means it does not
//	explicit#2  same breakpoint again; a hit here is what proves the breakpoint
//	            actually caches rather than merely being accepted
//	key         prompt_cache_key alone
//
// The prefix carries a per-run nonce so "cold" is genuinely cold. Without it the
// previous run leaves the prefix cached and every baseline reports a hit, which
// is how a probe quietly stops measuring anything.
//
// It talks to SAP AI Core directly rather than through the proxy, so the proxy's
// own field filtering cannot mask the answer.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shipengqi/sap-ai-core-proxy/internal/config"
	"github.com/shipengqi/sap-ai-core-proxy/internal/sapclient"
)

func main() {
	models := flag.String("models", "gpt-5.6-terra,gpt-4o", "comma-separated deployment model names")
	timeout := flag.Duration("timeout", 120*time.Second, "per-request timeout")
	salt := flag.String("salt", "", "prefix nonce; defaults to the current time so runs do not share cache state")
	flag.Parse()

	nonce := *salt
	if nonce == "" {
		nonce = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	auth := sapclient.NewAuthManager(cfg.SAPAICore.TokenURL, cfg.SAPAICore.ClientID, cfg.SAPAICore.ClientSecret)
	client := sapclient.NewClient(cfg.SAPAICore.BaseURL, cfg.SAPAICore.ResourceGroup, auth)
	deployments := sapclient.NewDeploymentManager(client)

	prefix := buildPrefix(nonce)
	fmt.Printf("probe prefix: %d chars (~%d tokens), nonce %s\n", len(prefix), len(prefix)/4, nonce)
	fmt.Printf("1024 tokens is the minimum either provider caches\n")

	for _, model := range strings.Split(*models, ",") {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		probeModel(client, deployments, model, prefix, *timeout)
	}
}

// buildPrefix returns a stable block comfortably above the 1024-token minimum.
// The sentences vary so nothing collapses under deduplication, and the nonce on
// the first line makes every run start from a different prefix.
func buildPrefix(nonce string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Probe run %s. You are a meticulous assistant. Follow this reference manual.\n\n", nonce)
	regions := []string{"north", "south", "east", "west"}
	teams := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "Section %d documents procedure %d for reconciling ledger entry %d against purchase order %d in the %s region, owned by team %s.\n",
			i, 1000+i, 7000+i, 3000+i, regions[i%len(regions)], teams[i%len(teams)])
	}
	return b.String()
}

type result struct {
	status   int
	prompt   int
	cached   int
	write    int
	errParam string
	body     string
}

func probeModel(client *sapclient.Client, deployments *sapclient.DeploymentManager, model, prefix string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	dep, err := deployments.GetDeployment(ctx, model)
	cancel()
	if err != nil {
		fmt.Printf("\n%s: no deployment (%v)\n", model, err)
		return
	}
	fmt.Printf("\n%s  (deployment %s)\n", model, dep.ID)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"cold", chatBody(prefix, "alpha", nil)},
		{"warm", chatBody(prefix, "beta", nil)},
		{"explicit#1", explicitBody(prefix, "gamma")},
		{"explicit#2", explicitBody(prefix, "epsilon")},
		{"key", keyBody(prefix, "delta")},
	}

	results := make(map[string]result, len(cases))
	for _, tc := range cases {
		r := send(client, dep.DeployedURL, model, tc.body, timeout)
		results[tc.name] = r
		switch {
		case r.status == http.StatusOK:
			fmt.Printf("  %-11s HTTP 200   prompt=%-5d cached=%-5d write=%-5d\n", tc.name, r.prompt, r.cached, r.write)
		case r.status == http.StatusBadRequest && r.errParam != "":
			fmt.Printf("  %-11s HTTP 400   parameter rejected: %s\n", tc.name, r.errParam)
		default:
			fmt.Printf("  %-11s HTTP %d   %s\n", tc.name, r.status, truncate(r.body, 200))
		}
	}

	fmt.Printf("  -- %s\n", summarise(results))
}

func summarise(r map[string]result) string {
	var parts []string

	switch warm := r["warm"]; {
	case warm.status == http.StatusOK && warm.cached > 0:
		parts = append(parts, fmt.Sprintf("implicit caching works (%d of %d prompt tokens served from cache, no marker needed)", warm.cached, warm.prompt))
	case warm.status == http.StatusOK:
		parts = append(parts, "implicit caching did not hit")
	default:
		parts = append(parts, "implicit caching unproven")
	}

	switch {
	case r["explicit#1"].errParam == "prompt_cache_options":
		parts = append(parts, "explicit breakpoints NOT supported")
	case r["explicit#1"].status == http.StatusOK && r["explicit#2"].cached > 0:
		parts = append(parts, fmt.Sprintf("explicit breakpoints work (%d of %d cached on the repeat)", r["explicit#2"].cached, r["explicit#2"].prompt))
	case r["explicit#1"].status == http.StatusOK:
		parts = append(parts, "explicit parameters accepted but the breakpoint did not hit")
	default:
		parts = append(parts, "explicit breakpoints unproven")
	}

	switch k := r["key"]; {
	case k.status == http.StatusOK:
		parts = append(parts, "prompt_cache_key accepted")
	case k.errParam != "":
		parts = append(parts, "prompt_cache_key rejected")
	}

	return strings.Join(parts, "; ")
}

func chatBody(prefix, word string, extra map[string]any) map[string]any {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": prefix},
			map[string]any{"role": "user", "content": "Reply with one word: " + word},
		},
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func keyBody(prefix, word string) map[string]any {
	return chatBody(prefix, word, map[string]any{"prompt_cache_key": "aicore-cacheprobe:v1"})
}

// explicitBody puts the breakpoint on the system block, which is the part that
// repeats across turns.
func explicitBody(prefix, word string) map[string]any {
	return map[string]any{
		"prompt_cache_key":     "aicore-cacheprobe:v1",
		"prompt_cache_options": map[string]any{"mode": "explicit", "ttl": "30m"},
		"messages": []any{
			map[string]any{"role": "system", "content": []any{
				map[string]any{
					"type":                    "text",
					"text":                    prefix,
					"prompt_cache_breakpoint": map[string]any{"mode": "explicit"},
				},
			}},
			map[string]any{"role": "user", "content": "Reply with one word: " + word},
		},
	}
}

func send(client *sapclient.Client, deployedURL, model string, body map[string]any, timeout time.Duration) result {
	body["model"] = model
	body["max_completion_tokens"] = 16
	raw, _ := json.Marshal(body)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	resp, err := client.Do(ctx, http.MethodPost,
		deployedURL+"/chat/completions?api-version=2024-02-01", strings.NewReader(string(raw)), nil)
	if err != nil {
		return result{status: -1, body: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	payload, _ := io.ReadAll(resp.Body)

	var parsed struct {
		Error struct {
			Param string `json:"param"`
		} `json:"error"`
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			PromptTokensDetails struct {
				CachedTokens     int `json:"cached_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(payload, &parsed)

	return result{
		status:   resp.StatusCode,
		prompt:   parsed.Usage.PromptTokens,
		cached:   parsed.Usage.PromptTokensDetails.CachedTokens,
		write:    parsed.Usage.PromptTokensDetails.CacheWriteTokens,
		errParam: parsed.Error.Param,
		body:     string(payload),
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

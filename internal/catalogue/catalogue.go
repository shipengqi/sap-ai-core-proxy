package catalogue

import (
	"fmt"
	"strings"
)

// ModelEntry holds all metadata for a model in the SAP AI Core catalogue.
type ModelEntry struct {
	SAPName           string
	Provider          string
	MaxTokens         int
	ContextWindow     int
	SupportsStreaming bool
	SupportsVision    bool
	UsesConverseAPI   bool
	AnthropicAliases  []string
	Family            string // "opus" | "sonnet" | "haiku" for Anthropic models; "" otherwise
}

var entries = []ModelEntry{
	// Claude 4.8
	{SAPName: "anthropic--claude-4.8-opus", Provider: "anthropic", MaxTokens: 32768, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-opus-4-8"}, Family: "opus"},
	// Claude 4.7
	{SAPName: "anthropic--claude-4.7-opus", Provider: "anthropic", MaxTokens: 32768, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-opus-4-7"}, Family: "opus"},
	// Claude 4.6
	{SAPName: "anthropic--claude-4.6-sonnet", Provider: "anthropic", MaxTokens: 32768, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-sonnet-4-6"}, Family: "sonnet"},
	{SAPName: "anthropic--claude-4.6-opus", Provider: "anthropic", MaxTokens: 32768, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-opus-4-6"}, Family: "opus"},
	{SAPName: "anthropic--claude-4.6-haiku", Provider: "anthropic", MaxTokens: 32768, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-haiku-4-6"}, Family: "haiku"},
	// Claude 4.5
	{SAPName: "anthropic--claude-4.5-sonnet", Provider: "anthropic", MaxTokens: 16384, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-sonnet-4-5"}, Family: "sonnet"},
	{SAPName: "anthropic--claude-4.5-opus", Provider: "anthropic", MaxTokens: 16384, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-opus-4-5"}, Family: "opus"},
	{SAPName: "anthropic--claude-4.5-haiku", Provider: "anthropic", MaxTokens: 16384, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-haiku-4-5"}, Family: "haiku"},
	// Claude 4
	{SAPName: "anthropic--claude-4-sonnet", Provider: "anthropic", MaxTokens: 16384, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-sonnet-4"}, Family: "sonnet"},
	{SAPName: "anthropic--claude-4-opus", Provider: "anthropic", MaxTokens: 16384, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-opus-4"}, Family: "opus"},
	// Claude 3.7
	{SAPName: "anthropic--claude-3.7-sonnet", Provider: "anthropic", MaxTokens: 8192, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-3-7-sonnet-20250219", "claude-3-7-sonnet-latest"}, Family: "sonnet"},
	// Claude 3.5
	{SAPName: "anthropic--claude-3.5-sonnet", Provider: "anthropic", MaxTokens: 8192, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-3-5-sonnet-20241022", "claude-3-5-sonnet-20240620", "claude-3-5-sonnet-latest"}, Family: "sonnet"},
	{SAPName: "anthropic--claude-3.5-haiku", Provider: "anthropic", MaxTokens: 8192, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: true, AnthropicAliases: []string{"claude-3-5-haiku-20241022", "claude-3-5-haiku-latest"}, Family: "haiku"},
	// Claude 3 (Invoke path — no Converse)
	{SAPName: "anthropic--claude-3-opus", Provider: "anthropic", MaxTokens: 4096, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: false, AnthropicAliases: []string{"claude-3-opus-20240229", "claude-3-opus-latest"}, Family: "opus"},
	{SAPName: "anthropic--claude-3-sonnet", Provider: "anthropic", MaxTokens: 4096, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: false, AnthropicAliases: []string{"claude-3-sonnet-20240229"}, Family: "sonnet"},
	{SAPName: "anthropic--claude-3-haiku", Provider: "anthropic", MaxTokens: 4096, ContextWindow: 200000, SupportsStreaming: true, SupportsVision: true, UsesConverseAPI: false, AnthropicAliases: []string{"claude-3-haiku-20240307"}, Family: "haiku"},
	// OpenAI
	{SAPName: "gpt-4o", Provider: "openai", MaxTokens: 16384, ContextWindow: 128000, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gpt-4o-mini", Provider: "openai", MaxTokens: 16384, ContextWindow: 128000, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gpt-4", Provider: "openai", MaxTokens: 8192, ContextWindow: 8192, SupportsStreaming: true},
	{SAPName: "gpt-4-32k", Provider: "openai", MaxTokens: 8192, ContextWindow: 32768, SupportsStreaming: true},
	{SAPName: "gpt-4.1", Provider: "openai", MaxTokens: 32768, ContextWindow: 1047576, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gpt-4.1-nano", Provider: "openai", MaxTokens: 32768, ContextWindow: 1047576, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gpt-5", Provider: "openai", MaxTokens: 100000, ContextWindow: 1047576, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gpt-5-nano", Provider: "openai", MaxTokens: 100000, ContextWindow: 1047576, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gpt-5-mini", Provider: "openai", MaxTokens: 100000, ContextWindow: 1047576, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gpt-35-turbo", Provider: "openai", MaxTokens: 4096, ContextWindow: 16385, SupportsStreaming: true},
	{SAPName: "gpt-35-turbo-16k", Provider: "openai", MaxTokens: 4096, ContextWindow: 16384, SupportsStreaming: true},
	{SAPName: "gpt-35-turbo-0125", Provider: "openai", MaxTokens: 4096, ContextWindow: 16385, SupportsStreaming: true},
	{SAPName: "o1", Provider: "openai", MaxTokens: 100000, ContextWindow: 200000, SupportsStreaming: true},
	{SAPName: "o3-mini", Provider: "openai", MaxTokens: 100000, ContextWindow: 200000},
	{SAPName: "o3", Provider: "openai", MaxTokens: 100000, ContextWindow: 200000, SupportsStreaming: true},
	{SAPName: "o4-mini", Provider: "openai", MaxTokens: 100000, ContextWindow: 200000, SupportsStreaming: true},
	// Gemini
	{SAPName: "gemini-2.5-pro", Provider: "gemini", MaxTokens: 65536, ContextWindow: 2097152, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gemini-2.5-flash", Provider: "gemini", MaxTokens: 65536, ContextWindow: 1048576, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gemini-2.5-flash-lite", Provider: "gemini", MaxTokens: 65536, ContextWindow: 1048576, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gemini-2.5-flash-image", Provider: "gemini", MaxTokens: 65536, ContextWindow: 1048576, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gemini-2.0-flash", Provider: "gemini", MaxTokens: 65536, ContextWindow: 1048576, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gemini-2.0-flash-lite", Provider: "gemini", MaxTokens: 65536, ContextWindow: 1048576, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gemini-1.5-pro", Provider: "gemini", MaxTokens: 8192, ContextWindow: 2097152, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gemini-1.5-flash", Provider: "gemini", MaxTokens: 8192, ContextWindow: 1048576, SupportsStreaming: true, SupportsVision: true},
	{SAPName: "gemini-1.0-pro", Provider: "gemini", MaxTokens: 8192, ContextWindow: 32760, SupportsStreaming: true},
	// Perplexity
	{SAPName: "sonar-pro", Provider: "openai", MaxTokens: 8192, ContextWindow: 200000, SupportsStreaming: true},
	{SAPName: "sonar", Provider: "openai", MaxTokens: 8192, ContextWindow: 127072, SupportsStreaming: true},
	{SAPName: "sonar-deep-research", Provider: "openai", MaxTokens: 8192, ContextWindow: 200000, SupportsStreaming: true},
	// Meta (Llama)
	{SAPName: "meta--llama3-70b-instruct", Provider: "meta", MaxTokens: 8192, ContextWindow: 8192, SupportsStreaming: true},
	{SAPName: "meta--llama3.1-70b-instruct", Provider: "meta", MaxTokens: 8192, ContextWindow: 128000, SupportsStreaming: true},
	// Mistral
	{SAPName: "mistralai--mixtral-8x7b-instruct-v01", Provider: "mistral", MaxTokens: 32768, ContextWindow: 32768, SupportsStreaming: true},
	{SAPName: "mistralai--mistral-large-instruct", Provider: "mistral", MaxTokens: 32768, ContextWindow: 128000, SupportsStreaming: true, AnthropicAliases: []string{"mistralai--mistral-large-instruct-2407"}},
	{SAPName: "mistralai--mistral-medium-instruct", Provider: "mistral", MaxTokens: 32768, ContextWindow: 128000, SupportsStreaming: true},
	// Cohere
	{SAPName: "cohere--command-a-reasoning", Provider: "openai", MaxTokens: 8192, ContextWindow: 256000, SupportsStreaming: true},
	// Amazon
	{SAPName: "amazon--nova-lite", Provider: "openai", MaxTokens: 5120, ContextWindow: 300000, SupportsStreaming: true, SupportsVision: true},
}

var bySAPName map[string]*ModelEntry
var byAnthropicAlias map[string]*ModelEntry

func init() {
	bySAPName = make(map[string]*ModelEntry, len(entries))
	byAnthropicAlias = make(map[string]*ModelEntry)
	for i := range entries {
		e := &entries[i]
		bySAPName[e.SAPName] = e
		for _, alias := range e.AnthropicAliases {
			byAnthropicAlias[alias] = e
		}
	}

	// Dynamically wire -latest aliases to the first (newest) entry per family.
	// entries is ordered newest-first, so the first match per family wins.
	latestFamilies := []string{"opus", "sonnet", "haiku"}
	for _, family := range latestFamilies {
		latestAlias := "claude-" + family + "-latest"
		if _, already := byAnthropicAlias[latestAlias]; already {
			continue
		}
		for i := range entries {
			e := &entries[i]
			if e.Family == family {
				byAnthropicAlias[latestAlias] = e
				break
			}
		}
	}
}

func Get(sapName string) (*ModelEntry, bool) {
	e, ok := bySAPName[sapName]
	return e, ok
}

func MustGet(sapName string) *ModelEntry {
	e, ok := bySAPName[sapName]
	if !ok {
		panic(fmt.Sprintf("unknown SAP model %q — add it to internal/catalogue/catalogue.go", sapName))
	}
	return e
}

func UsesConverseAPI(sapName string) bool {
	e, ok := bySAPName[sapName]
	return ok && e.UsesConverseAPI
}

func GetProvider(sapName string) string {
	if e, ok := bySAPName[sapName]; ok {
		return e.Provider
	}
	return ""
}

func GetOwner(sapName string) string {
	p := GetProvider(sapName)
	if p == "" {
		return "sap-ai-core"
	}
	if p == "gemini" {
		return "google"
	}
	return p
}

// MapFromAnthropic maps an Anthropic SDK model name (or a direct SAP name with "--")
// to the canonical SAP name. Returns an error for unknown names.
func MapFromAnthropic(name string) (string, error) {
	// Direct SAP name passthrough
	if len(name) > 2 && name[0:2] != "cl" {
		// Check if it contains "--" which indicates a SAP name
		for i := 0; i < len(name)-1; i++ {
			if name[i] == '-' && name[i+1] == '-' {
				if _, ok := bySAPName[name]; ok {
					return name, nil
				}
				return "", fmt.Errorf("unknown SAP model %q", name)
			}
		}
	}
	if e, ok := byAnthropicAlias[name]; ok {
		return e.SAPName, nil
	}
	// Also check if it's a direct SAP name
	if _, ok := bySAPName[name]; ok {
		return name, nil
	}
	return "", fmt.Errorf("unknown Anthropic model %q — add it to internal/catalogue/catalogue.go", name)
}

// ResolveChain returns the ordered SAP names to try for a given Anthropic alias.
// For -latest aliases it returns all models in the same family (newest-first),
// so callers can fall back to the next available deployment if the preferred one
// is not yet deployed. For specific version aliases it returns a single-element slice.
func ResolveChain(name string) ([]string, error) {
	preferred, err := MapFromAnthropic(name)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(name, "-latest") {
		return []string{preferred}, nil
	}
	// Build the full family chain (entries are newest-first).
	family := bySAPName[preferred].Family
	if family == "" {
		return []string{preferred}, nil
	}
	var chain []string
	for i := range entries {
		if entries[i].Family == family {
			chain = append(chain, entries[i].SAPName)
		}
	}
	return chain, nil
}

func ListAll() []ModelEntry {
	result := make([]ModelEntry, len(entries))
	copy(result, entries)
	return result
}

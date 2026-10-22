package transform

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestHasCacheControl(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "absent", body: `{"tools":[]}`, want: false},
		{name: "top level null", body: `{"cache_control":null}`, want: true},
		{name: "deep object", body: `{"messages":[{"content":[{"cache_control":{"type":"ephemeral"}}]}]}`, want: true},
		{name: "nested array", body: `{"metadata":[[{"cache_control":false}]]}`, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.body), &raw); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got := HasCacheControl(raw); got != tc.want {
				t.Errorf("HasCacheControl() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplyAnthropicToolCacheMarker(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		enabled      bool
		clientMarked bool
		wantAdded    bool
		wantResult   string
		wantMarker   bool
	}{
		{
			name:       "adds final tool marker",
			body:       `{"tools":[{"name":"first"},{"name":"last"}]}`,
			enabled:    true,
			wantAdded:  true,
			wantResult: AutoMarkerAdded,
			wantMarker: true,
		},
		{
			name:       "disabled reports nothing",
			body:       `{"tools":[{"name":"last"}]}`,
			enabled:    false,
			wantAdded:  false,
			wantResult: "",
			wantMarker: false,
		},
		{
			name:         "client marked",
			body:         `{"tools":[{"name":"last"}]}`,
			enabled:      true,
			clientMarked: true,
			wantAdded:    false,
			wantResult:   AutoMarkerSkippedClient,
			wantMarker:   false,
		},
		{
			name:       "existing deep marker",
			body:       `{"messages":[{"content":[{"cache_control":null}]}],"tools":[{"name":"last"}]}`,
			enabled:    true,
			wantAdded:  false,
			wantResult: AutoMarkerSkippedClient,
			wantMarker: false,
		},
		{
			name:       "missing tools",
			body:       `{"messages":[{"content":"hi"}]}`,
			enabled:    true,
			wantAdded:  false,
			wantResult: AutoMarkerSkippedNoTools,
			wantMarker: false,
		},
		{
			name:       "empty tools",
			body:       `{"tools":[]}`,
			enabled:    true,
			wantAdded:  false,
			wantResult: AutoMarkerSkippedNoTools,
			wantMarker: false,
		},
		{
			name:       "invalid tools",
			body:       `{"tools":{"name":"last"}}`,
			enabled:    true,
			wantAdded:  false,
			wantResult: AutoMarkerSkippedNoTools,
			wantMarker: false,
		},
		{
			name:       "invalid final tool",
			body:       `{"tools":[{"name":"first"},null]}`,
			enabled:    true,
			wantAdded:  false,
			wantResult: AutoMarkerSkippedNoTools,
			wantMarker: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.body), &raw); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			added, result := ApplyAnthropicToolCacheMarker(raw, tc.enabled, tc.clientMarked)
			if added != tc.wantAdded {
				t.Errorf("added = %v, want %v", added, tc.wantAdded)
			}
			if result != tc.wantResult {
				t.Errorf("result = %q, want %q", result, tc.wantResult)
			}
			var tools []map[string]json.RawMessage
			_ = json.Unmarshal(raw["tools"], &tools)
			marked := false
			if len(tools) > 0 {
				_, marked = tools[len(tools)-1]["cache_control"]
			}
			if marked != tc.wantMarker {
				t.Errorf("final tool marked = %v, want %v", marked, tc.wantMarker)
			}
		})
	}
}

func TestApplyAnthropicToolCacheMarkerIsIdempotentAndPreservesMessageFields(t *testing.T) {
	body := []byte(`{"system":"keep this string","messages":[{"role":"user","content":"raw message"}],"tools":[{"name":"last"}]}`)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	systemBefore := append([]byte(nil), raw["system"]...)
	messagesBefore := append([]byte(nil), raw["messages"]...)
	added, result := ApplyAnthropicToolCacheMarker(raw, true, false)
	if !added || result != AutoMarkerAdded {
		t.Fatalf("first application = (%v, %q), want (true, %q)", added, result, AutoMarkerAdded)
	}
	// The second application sees the marker it just wrote and must report the
	// client-marker skip rather than adding a second one.
	added, result = ApplyAnthropicToolCacheMarker(raw, true, false)
	if added || result != AutoMarkerSkippedClient {
		t.Fatalf("second application = (%v, %q), want (false, %q)", added, result, AutoMarkerSkippedClient)
	}
	if !bytes.Equal(raw["system"], systemBefore) {
		t.Errorf("system changed: got %s, want %s", raw["system"], systemBefore)
	}
	if !bytes.Equal(raw["messages"], messagesBefore) {
		t.Errorf("messages changed: got %s, want %s", raw["messages"], messagesBefore)
	}
}

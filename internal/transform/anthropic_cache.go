package transform

import "encoding/json"

// Outcomes of ApplyAnthropicToolCacheMarker, published as the result label of
// sap_proxy_cache_auto_marker_total.
//
// The empty string is deliberately not one of them: it means "no signal at
// all", which is what a disabled feature returns so that nothing is counted.
const (
	// AutoMarkerAdded means the ephemeral marker was added to the final tool.
	AutoMarkerAdded = "added"
	// AutoMarkerSkippedClient means the client already placed a cache_control
	// marker somewhere in the request, so the proxy did not touch it.
	AutoMarkerSkippedClient = "skipped_client_marker"
	// AutoMarkerSkippedNoTools means there was no final tool declaration to
	// mark: tools is absent, not an array, empty, or its last item is not a
	// JSON object.
	AutoMarkerSkippedNoTools = "skipped_no_tools"
)

// HasCacheControl reports whether cache_control occurs at any depth in a JSON object.
// A present key counts even when its value is null or malformed JSON.
func HasCacheControl(m map[string]json.RawMessage) bool {
	for key, value := range m {
		if key == "cache_control" || hasCacheControlRaw(value) {
			return true
		}
	}
	return false
}

func hasCacheControlRaw(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		return HasCacheControl(object)
	}
	var array []json.RawMessage
	if json.Unmarshal(raw, &array) != nil {
		return false
	}
	for _, item := range array {
		if hasCacheControlRaw(item) {
			return true
		}
	}
	return false
}

// ApplyAnthropicToolCacheMarker adds an ephemeral cache marker to the final valid
// Anthropic tool declaration. It never changes a client-marked request or malformed
// tools payload.
//
// It returns whether the marker was added plus the outcome to publish as a metric
// label. A disabled feature returns (false, "") and therefore emits no signal at
// all; every other path returns one of the AutoMarker* constants. The marshal
// failures are unreachable — the values being marshalled are JSON that was just
// decoded — and return no result rather than a misleading one.
func ApplyAnthropicToolCacheMarker(m map[string]json.RawMessage, enabled bool, clientMarked bool) (bool, string) {
	// Checked first on purpose: a disabled feature must not even walk the body,
	// and it must not report a skip it never considered.
	if !enabled {
		return false, ""
	}
	if clientMarked || HasCacheControl(m) {
		return false, AutoMarkerSkippedClient
	}

	toolsRaw, ok := m["tools"]
	if !ok {
		return false, AutoMarkerSkippedNoTools
	}
	var tools []json.RawMessage
	if json.Unmarshal(toolsRaw, &tools) != nil || len(tools) == 0 {
		return false, AutoMarkerSkippedNoTools
	}
	lastIndex := len(tools) - 1
	var tool map[string]json.RawMessage
	if json.Unmarshal(tools[lastIndex], &tool) != nil || tool == nil {
		return false, AutoMarkerSkippedNoTools
	}
	marker, err := json.Marshal(map[string]string{"type": "ephemeral"})
	if err != nil {
		return false, ""
	}
	tool["cache_control"] = marker
	tools[lastIndex], err = json.Marshal(tool)
	if err != nil {
		return false, ""
	}
	updated, err := json.Marshal(tools)
	if err != nil {
		return false, ""
	}
	m["tools"] = updated
	return true, AutoMarkerAdded
}

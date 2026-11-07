package agentconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// mergeEnvJSON merges variables into settings.json's "env" object while
// preserving every unrelated top-level key and every unrelated env var.
// Claude Code settings.json is strict JSON, so a full decode/encode is safe;
// unknown values are kept as json.RawMessage.
func mergeEnvJSON(old []byte, values map[string]string) ([]byte, error) {
	root := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(old)) > 0 {
		if err := json.Unmarshal(old, &root); err != nil {
			return nil, fmt.Errorf("parse settings.json: %w", err)
		}
	}
	env := map[string]json.RawMessage{}
	if raw, ok := root["env"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("parse settings.json env: %w", err)
		}
	}
	for key, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		env[key] = encoded
	}
	encodedEnv, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return nil, err
	}
	root["env"] = encodedEnv
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// mergeJSONObject is the same idea for arbitrary JSON files (used by auth.json).
func mergeJSONObject(old []byte, values map[string]string) ([]byte, error) {
	root := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(old)) > 0 {
		if err := json.Unmarshal(old, &root); err != nil {
			return nil, fmt.Errorf("parse JSON: %w", err)
		}
	}
	for key, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		root[key] = encoded
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

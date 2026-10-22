package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

var hiddenPollingPaths = map[string]bool{
	"/health":         true,
	"/stats":          true,
	"/stats/cache":    true,
	"/stats/compress": true,
}

type diagnosticLogLine struct {
	Text  string
	Level string
}

// formatLogRecords turns slog JSON into a compact diagnostic view. Important
// long fields are emitted on their own lines so terminal wrapping preserves the
// complete upstream error instead of hiding it behind an ellipsis. Every line
// retains its source level so wrapped detail rows receive the same color.
func formatLogRecords(lines []string) []diagnosticLogLine {
	var out []diagnosticLogLine
	for _, line := range lines {
		formatted, visible := formatLogRecord(line)
		if !visible {
			continue
		}
		level := logLevel(line)
		for _, text := range formatted {
			out = append(out, diagnosticLogLine{Text: text, Level: level})
		}
	}
	return out
}

func logLevel(line string) string {
	var record struct {
		Level string `json:"level"`
	}
	if json.Unmarshal([]byte(line), &record) != nil {
		return "INFO"
	}
	level := strings.ToUpper(record.Level)
	if level == "" {
		return "INFO"
	}
	return level
}

func formatLogRecord(line string) ([]string, bool) {
	clean := strings.ReplaceAll(strings.TrimSuffix(line, "\r"), "\t", "    ")
	var record map[string]any
	if json.Unmarshal([]byte(clean), &record) != nil {
		return []string{clean}, true
	}

	msg := textField(record, "msg")
	method := textField(record, "method")
	path := textField(record, "path")
	if msg == "request" && method == "GET" && hiddenPollingPaths[path] {
		return nil, false
	}

	level := strings.ToUpper(textField(record, "level"))
	if level == "" {
		level = "INFO"
	}
	stamp := formatLogTime(textField(record, "time"))
	prefix := strings.TrimSpace(stamp + " " + level)
	first := strings.TrimSpace(prefix + "  " + msg)

	used := map[string]bool{"time": true, "level": true, "msg": true, "err": true, "body": true}
	ordered := []string{"method", "path", "status", "model", "streaming", "deployment_id", "latency_ms", "client_ip"}
	for _, key := range ordered {
		if value, ok := record[key]; ok {
			first += "  " + key + "=" + displayValue(value)
			used[key] = true
		}
	}

	var details []string
	if value := textField(record, "url"); value != "" {
		details = append(details, "  url: "+value)
		used["url"] = true
	}
	if value := textField(record, "err"); value != "" {
		details = append(details, "  error: "+value)
	}
	if value := textField(record, "body"); value != "" {
		details = append(details, "  body: "+value)
	}

	var extraKeys []string
	for key := range record {
		if !used[key] {
			extraKeys = append(extraKeys, key)
		}
	}
	sort.Strings(extraKeys)
	for _, key := range extraKeys {
		first += "  " + key + "=" + displayValue(record[key])
	}

	return append([]string{first}, details...), true
}

func textField(record map[string]any, key string) string {
	value, _ := record[key].(string)
	return value
}

func displayValue(value any) string {
	switch v := value.(type) {
	case string:
		if strings.ContainsAny(v, " \t\r\n") {
			return fmt.Sprintf("%q", v)
		}
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%t", v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(encoded)
	}
}

func formatLogTime(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.Local().Format("15:04:05.000")
}

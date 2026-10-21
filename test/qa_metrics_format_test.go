package test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// qaSample is one parsed Prometheus sample line.
type qaSample struct {
	name   string
	labels map[string]string
	value  float64
	raw    string
}

// qaParseMetrics parses the Prometheus text exposition format with a minimal,
// dependency-free parser.
//
// It is intentionally strict: every non-comment line must be exactly
// `name{labels} value` (labels optional), the value must parse as a float, the
// metric name must match the exposition grammar, label values must only use the
// three legal escapes, every sample must have a # TYPE declaration, and no
// (name, labels) pair may repeat — Prometheus rejects duplicate series, so a
// duplicated line is a broken scrape even though it "looks" fine.
func qaParseMetrics(t *testing.T, text string) []qaSample {
	t.Helper()

	declared := map[string]string{}
	seen := map[string]bool{}
	var samples []qaSample

	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#") {
			fields := strings.SplitN(line, " ", 4)
			switch {
			case len(fields) >= 4 && fields[1] == "TYPE":
				switch fields[3] {
				case "counter", "gauge", "summary", "histogram", "untyped":
				default:
					t.Fatalf("invalid metric type %q in line %q", fields[3], line)
				}
				if !qaValidMetricName(fields[2]) {
					t.Fatalf("invalid metric name %q in line %q", fields[2], line)
				}
				declared[fields[2]] = fields[3]
			case len(fields) >= 4 && fields[1] == "HELP":
				if !qaValidMetricName(fields[2]) {
					t.Fatalf("invalid metric name %q in line %q", fields[2], line)
				}
			default:
				t.Fatalf("malformed comment line %q", line)
			}
			continue
		}

		s, err := qaParseSample(line)
		if err != nil {
			t.Fatalf("unparseable sample line %q: %v", line, err)
		}
		if _, ok := declared[s.name]; !ok {
			base := strings.TrimSuffix(strings.TrimSuffix(s.name, "_sum"), "_count")
			if _, ok := declared[base]; !ok {
				t.Fatalf("series %q has no # TYPE declaration (line %q)", s.name, line)
			}
		}

		key := s.name + "\x00" + qaLabelKey(s.labels)
		if seen[key] {
			t.Fatalf("duplicate series %q (line %q)", key, line)
		}
		seen[key] = true
		samples = append(samples, s)
	}

	return samples
}

func qaParseSample(line string) (qaSample, error) {
	s := qaSample{labels: map[string]string{}, raw: line}

	i := 0
	for i < len(line) && (isAlnum(line[i]) || line[i] == '_' || line[i] == ':') {
		i++
	}
	s.name = line[:i]
	if !qaValidMetricName(s.name) {
		return s, fmt.Errorf("invalid metric name %q", s.name)
	}

	if i < len(line) && line[i] == '{' {
		i++
		for {
			if i >= len(line) {
				return s, fmt.Errorf("unterminated label set")
			}
			if line[i] == '}' {
				i++
				break
			}
			start := i
			for i < len(line) && line[i] != '=' {
				i++
			}
			if i >= len(line) {
				return s, fmt.Errorf("label %q has no value", line[start:i])
			}
			name := line[start:i]
			if name == "" {
				return s, fmt.Errorf("empty label name")
			}
			i++ // '='
			if i >= len(line) || line[i] != '"' {
				return s, fmt.Errorf("label %q value is not quoted", name)
			}
			i++
			var value strings.Builder
			for {
				if i >= len(line) {
					return s, fmt.Errorf("unterminated label value for %q", name)
				}
				c := line[i]
				if c == '\\' {
					if i+1 >= len(line) {
						return s, fmt.Errorf("dangling escape in label %q", name)
					}
					switch line[i+1] {
					case 'n':
						value.WriteByte('\n')
					case '"':
						value.WriteByte('"')
					case '\\':
						value.WriteByte('\\')
					default:
						return s, fmt.Errorf("illegal escape %q in label %q", string(line[i:i+2]), name)
					}
					i += 2
					continue
				}
				if c == '"' {
					i++
					break
				}
				value.WriteByte(c)
				i++
			}
			s.labels[name] = value.String()

			if i >= len(line) {
				return s, fmt.Errorf("unterminated label set")
			}
			switch line[i] {
			case ',':
				i++
			case '}':
				i++
			default:
				return s, fmt.Errorf("unexpected %q after label %q", string(line[i]), name)
			}
			if line[i-1] == '}' {
				break
			}
		}
	}

	if i >= len(line) || line[i] != ' ' {
		return s, fmt.Errorf("expected a space between the series and its value")
	}
	i++
	valueText := line[i:]
	if valueText == "" {
		return s, fmt.Errorf("missing value")
	}
	v, err := strconv.ParseFloat(valueText, 64)
	if err != nil {
		return s, fmt.Errorf("value %q is not a number: %w", valueText, err)
	}
	s.value = v
	return s, nil
}

func qaValidMetricName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case isAlnum(c) || c == '_' || c == ':':
		case i == 0 && (c >= '0' && c <= '9'):
			return false
		default:
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func qaLabelKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	// Deterministic ordering; the label count is tiny.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(labels[k])
		b.WriteString(";")
	}
	return b.String()
}

// qaFindSample returns the samples whose name and labels match.
func qaFindSamples(samples []qaSample, name string, labels map[string]string) []qaSample {
	var out []qaSample
	for _, s := range samples {
		if s.name != name {
			continue
		}
		match := true
		for k, v := range labels {
			if s.labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, s)
		}
	}
	return out
}

// TestQA_MetricsFormatIsValid drives a real request through the proxy and then
// validates the whole /metrics payload line by line.
func TestQA_MetricsFormatIsValid(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyNonStreaming); status != 200 {
		t.Fatalf("seed request failed: %d %s", status, body)
	}
	if status, body := qaPost(t, proxy.URL+"/anthropic/v1/messages", qaClientBodyStreaming); status != 200 {
		t.Fatalf("seed stream failed: %d %s", status, body)
	}

	resp, err := http.Get(proxy.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("Content-Type = %q, want the Prometheus 0.0.4 text format", ct)
	}

	text := qaGetMetrics(t, proxy.URL)
	samples := qaParseMetrics(t, text)

	// Every metric family the package promises must be declared and emitted.
	for _, name := range []string{
		"sap_proxy_requests_total",
		"sap_proxy_input_tokens_total",
		"sap_proxy_output_tokens_total",
		"sap_proxy_cache_read_tokens_total",
		"sap_proxy_cache_write_tokens_total",
		"sap_proxy_usage_missing_total",
		"sap_proxy_uninstrumented_total",
		"sap_proxy_usage_capture_dropped_total",
		"sap_proxy_request_duration_seconds",
		"sap_proxy_uptime_seconds",
	} {
		if !strings.Contains(text, "# TYPE "+name+" ") {
			t.Errorf("missing # TYPE declaration for %s", name)
		}
	}

	if got := len(qaFindSamples(samples, "sap_proxy_uptime_seconds", nil)); got != 1 {
		t.Errorf("uptime samples = %d, want 1", got)
	}
	// status/stream split: one non-streaming and one streaming 200.
	for _, stream := range []string{"false", "true"} {
		got := qaFindSamples(samples, "sap_proxy_requests_total", map[string]string{
			"surface": "anthropic", "status": "200", "stream": stream,
		})
		if len(got) != 1 || got[0].value != 1 {
			t.Errorf("requests_total{stream=%q} = %+v, want one series with value 1", stream, got)
		}
	}
	if got := qaFindSamples(samples, "sap_proxy_input_tokens_total", map[string]string{"surface": "anthropic"}); len(got) != 1 || got[0].value != 20 {
		t.Errorf("input_tokens = %+v, want one series with value 20", got)
	}
	if got := qaFindSamples(samples, "sap_proxy_request_duration_seconds_count", map[string]string{"surface": "anthropic"}); len(got) != 1 || got[0].value != 2 {
		t.Errorf("duration count = %+v, want one series with value 2", got)
	}
}

// TestQA_MetricsLabelInjectionIsEscaped feeds a model name built to break the
// exposition format and asserts the payload still parses and the label survives
// intact.
func TestQA_MetricsLabelInjectionIsEscaped(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	// A quote closes the label, a newline splits the series, braces start a fake
	// label set, and a backslash starts a fake escape.
	hostile := "evil\"}\nfoo{bar=\"x\\y\r"

	body, err := json.Marshal(map[string]any{
		"model":      hostile,
		"max_tokens": 8,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	// The model resolves to no deployment, so the request fails — but the handler
	// sets the model label before it resolves the deployment, which is exactly the
	// path a hostile caller controls.
	status, respBody := qaPost(t, proxy.URL+"/anthropic/v1/messages", string(body))
	if status < 400 {
		t.Fatalf("expected the request to fail for an unknown model, got %d: %s", status, respBody)
	}

	doc := qaGetStats(t, proxy.URL)
	model := qaModelOf(t, doc)
	if model.Model != hostile {
		t.Fatalf("by_model label = %q, want the raw model name %q", model.Model, hostile)
	}

	text := qaGetMetrics(t, proxy.URL)
	samples := qaParseMetrics(t, text) // fails the test if any line is malformed

	// The escaped newline must not have produced a stray blank line.
	if !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n") {
		t.Errorf("the payload must end with exactly one newline, got %q", text[len(text)-4:])
	}

	// Every family that carries the model label must carry it intact.
	for _, name := range []string{
		"sap_proxy_requests_total",
		"sap_proxy_input_tokens_total",
		"sap_proxy_output_tokens_total",
		"sap_proxy_cache_read_tokens_total",
		"sap_proxy_cache_write_tokens_total",
		"sap_proxy_usage_missing_total",
	} {
		got := qaFindSamples(samples, name, map[string]string{"model": hostile})
		if len(got) != 1 {
			t.Errorf("%s has %d series with the hostile model label, want 1\n%s", name, len(got), text)
		}
	}
}

// TestQA_MetricsOnlyLegalEscapes is the end-to-end check for the F5 decision: the
// exposition format defines exactly three escapes, so the payload must contain no
// other backslash sequence while a raw carriage return and tab survive as raw
// bytes (they are legal in a label value).
//
// The rule is enforced by qaParseMetrics above, which rejects anything else —
// exactly what the reference parser in prometheus/common does. The negative
// control at the bottom proves that this test can fail.
func TestQA_MetricsOnlyLegalEscapes(t *testing.T) {
	up := newQAUpstream(t)
	defer up.Close()
	proxy := qaProxy(t, up.URL)
	defer proxy.Close()

	// CR and TAB raw, plus a quote, a newline and a backslash that must be escaped.
	// The backslash is deliberately followed by 'b', so a literal "\r" or "\t" in
	// the payload can only come from an illegal escape.
	const hostile = "cr\rlf\ntab\there\"q\\b"

	body, err := json.Marshal(map[string]any{
		"model":      hostile,
		"max_tokens": 8,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if status, respBody := qaPost(t, proxy.URL+"/anthropic/v1/messages", string(body)); status < 400 {
		t.Fatalf("expected the unknown model to fail, got %d: %s", status, respBody)
	}

	text := qaGetMetrics(t, proxy.URL)
	samples := qaParseMetrics(t, text) // strict: illegal escapes are a hard failure

	// Raw CR and TAB must be present as raw bytes.
	if !strings.Contains(text, "\r") {
		t.Error("the payload lost the raw carriage return")
	}
	if !strings.Contains(text, "\t") {
		t.Error("the payload lost the raw tab")
	}
	// ...and no illegal escape sequence may appear.
	for _, illegal := range []string{"\\r", "\\t"} {
		if strings.Contains(text, illegal) {
			t.Errorf("payload contains the illegal escape %q, which makes the whole scrape fail\n%s", illegal, text)
		}
	}

	var found bool
	for _, s := range samples {
		if s.labels["model"] == hostile {
			found = true
		}
	}
	if !found {
		t.Fatalf("no series carries the hostile model label intact; escaping lost it\n%q", text)
	}
}

// TestQA_ParserRejectsIllegalEscapes is the negative control for the test above:
// the parser used by the metrics assertions must reject `\r`/`\t`/`\x` and accept
// exactly the three legal sequences. Without this, TestQA_MetricsOnlyLegalEscapes
// could pass on a parser that simply ignores escapes.
func TestQA_ParserRejectsIllegalEscapes(t *testing.T) {
	for _, illegal := range []string{`\r`, `\t`, `\x`, `\0`} {
		line := `sap_proxy_input_tokens_total{surface="openai",model="a` + illegal + `b"} 1`
		if _, err := qaParseSample(line); err == nil {
			t.Errorf("qaParseSample accepted the illegal escape %q in %q", illegal, line)
		}
	}

	for _, legal := range []string{`\\`, `\"`, `\n`} {
		line := `sap_proxy_input_tokens_total{surface="openai",model="a` + legal + `b"} 1`
		if _, err := qaParseSample(line); err != nil {
			t.Errorf("qaParseSample rejected the legal escape %q: %v", legal, err)
		}
	}
}

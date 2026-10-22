package usage

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPrometheusRender(t *testing.T) {
	c := NewCollector()
	c.Add(Record{
		Surface:  SurfaceAnthropic,
		Model:    "claude-3-5-sonnet-latest",
		Status:   200,
		Stream:   true,
		Tokens:   Tokens{Input: 10, Output: 4, CacheRead: 6, CacheWrite: 2},
		HasUsage: true,
		Observed: true,
		Latency:  250 * time.Millisecond,
	})
	c.Add(Record{
		Surface:  SurfaceAnthropic,
		Model:    "claude-3-5-sonnet-latest",
		Status:   200,
		Tokens:   Tokens{Input: 5},
		Observed: true,
		Latency:  250 * time.Millisecond,
	})

	out := c.Snapshot().Prometheus()

	wantLines := []string{
		"# TYPE sap_proxy_requests_total counter",
		"# TYPE sap_proxy_input_tokens_total counter",
		"# TYPE sap_proxy_output_tokens_total counter",
		"# TYPE sap_proxy_cache_read_tokens_total counter",
		"# TYPE sap_proxy_cache_write_tokens_total counter",
		"# TYPE sap_proxy_usage_missing_total counter",
		"# TYPE sap_proxy_uninstrumented_total counter",
		"# TYPE sap_proxy_usage_capture_dropped_total counter",
		"# TYPE sap_proxy_request_duration_seconds summary",
		`sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="200",stream="false"} 1`,
		`sap_proxy_requests_total{surface="anthropic",model="anthropic--claude-3.5-sonnet",status="200",stream="true"} 1`,
		`sap_proxy_input_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 15`,
		`sap_proxy_output_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 4`,
		`sap_proxy_cache_read_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 6`,
		`sap_proxy_cache_write_tokens_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 2`,
		// One of the two requests reported no usage.
		`sap_proxy_usage_missing_total{surface="anthropic",model="anthropic--claude-3.5-sonnet"} 1`,
		`sap_proxy_request_duration_seconds_sum{surface="anthropic"} 0.5`,
		`sap_proxy_request_duration_seconds_count{surface="anthropic"} 2`,
		"sap_proxy_uptime_seconds ",
	}

	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output is missing %q\n---\n%s", want, out)
		}
	}
}

// TestPrometheusLabelEscaping pins the exposition-format escaping rules: a quote,
// a backslash or a newline inside a model name must not corrupt the payload.
func TestPrometheusLabelEscaping(t *testing.T) {
	c := NewCollector()
	c.Add(Record{
		Surface:  SurfaceOpenAI,
		Model:    "evil\"model\\x\nline",
		Status:   200,
		HasUsage: true,
		Tokens:   Tokens{Input: 1},
	})

	out := c.Snapshot().Prometheus()

	want := `sap_proxy_input_tokens_total{surface="openai",model="evil\"model\\x\nline"} 1`
	if !strings.Contains(out, want) {
		t.Errorf("escaped label missing.\nwant: %s\ngot:\n%s", want, out)
	}

	// The escaped form must stay on one line: a raw newline would split the
	// series and break every scraper downstream.
	seriesLines := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "sap_proxy_input_tokens_total{") {
			seriesLines++
		}
	}
	if seriesLines != 1 {
		t.Errorf("expected exactly one input-token series line, got %d:\n%s", seriesLines, out)
	}
}

// TestPrometheusEscapesOnlyLegalSequences pins the escaping rule by *parsing* the
// payload, not by searching it for substrings.
//
// A substring check for `\r` is a false positive as soon as the model name itself
// contains a literal backslash followed by an "r": the backslash is escaped to
// `\\`, the output contains `\\r`, and `strings.Contains(out, "\\r")` matches even
// though the payload is correct. Round-tripping through a strict parser is the
// only assertion that cannot be fooled that way.
func TestPrometheusEscapesOnlyLegalSequences(t *testing.T) {
	models := []string{
		`quote"inside`,
		`back\slash`,
		"new\nline",
		"carriage\rreturn",
		"tab\there",
		// The regression case: literal backslash + letter, which must survive the
		// round trip and must not look like an illegal escape.
		`literal\rand\tand\xsequences`,
	}

	c := NewCollector()
	for _, model := range models {
		c.Add(Record{
			Surface:  SurfaceOpenAI,
			Model:    model,
			Status:   200,
			HasUsage: true,
			Observed: true,
			Tokens:   Tokens{Input: 1},
		})
	}

	out := c.Snapshot().Prometheus()

	samples, err := parseExposition(out)
	if err != nil {
		t.Fatalf("the payload does not parse with a strict parser: %v\n%s", err, out)
	}

	decoded := make([]string, 0, len(models))
	for _, s := range samples {
		if s.name == "sap_proxy_input_tokens_total" {
			decoded = append(decoded, s.labels["model"])
		}
	}

	// Round trip: each model name must decode back to exactly what went in.
	for _, want := range models {
		found := false
		for _, got := range decoded {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("model label %q did not survive the round trip; decoded: %q", want, decoded)
		}
	}
}

// TestExpositionParserRejectsIllegalEscapes pins the other half of the claim that
// justifies not escaping \r and \t: those sequences are illegal in the format, so
// emitting them would break the scrape. The parser under test is the strict one
// below, which implements only the three legal escapes.
func TestExpositionParserRejectsIllegalEscapes(t *testing.T) {
	for _, illegal := range []string{`\r`, `\t`, `\x`, `\0`, `\a`} {
		line := `sap_proxy_input_tokens_total{model="a` + illegal + `b"} 1`
		if _, err := parseExposition(line); err == nil {
			t.Errorf("the parser accepted the illegal escape %q; if this ever becomes legal, "+
				"labelEscaper must emit it (line: %q)", illegal, line)
		}
	}
	for _, legal := range []string{`\\`, `\"`, `\n`} {
		line := `sap_proxy_input_tokens_total{model="a` + legal + `b"} 1`
		if _, err := parseExposition(line); err != nil {
			t.Errorf("the parser rejected the legal escape %q: %v", legal, err)
		}
	}
}

// sample is one parsed exposition line.
type sample struct {
	name   string
	labels map[string]string
	value  string
}

// parseExposition parses the Prometheus text exposition format strictly: every
// non-comment line must be `name{labels} value`, the value must be a number, and a
// label value may only use the three escapes the format defines. This mirrors what
// a real scraper does, so a payload it rejects would fail in production too.
func parseExposition(text string) ([]sample, error) {
	var out []sample
	for _, line := range strings.Split(text, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s, err := parseSampleLine(line)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func parseSampleLine(line string) (sample, error) {
	s := sample{labels: map[string]string{}}

	i := 0
	for i < len(line) && line[i] != '{' && line[i] != ' ' {
		i++
	}
	s.name = line[:i]
	if s.name == "" {
		return s, fmt.Errorf("empty metric name in %q", line)
	}

	if i < len(line) && line[i] == '{' {
		i++
		for {
			if i >= len(line) {
				return s, fmt.Errorf("unterminated label set in %q", line)
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
				return s, fmt.Errorf("label without a value in %q", line)
			}
			name := line[start:i]
			i++ // '='
			if i >= len(line) || line[i] != '"' {
				return s, fmt.Errorf("label %q value is not quoted in %q", name, line)
			}
			i++ // opening quote
			value, next, err := decodeLabelValue(line, i)
			if err != nil {
				return s, err
			}
			s.labels[name] = value
			i = next
			if i >= len(line) {
				return s, fmt.Errorf("unterminated label set in %q", line)
			}
			switch line[i] {
			case ',':
				i++
			case '}':
				i++
			default:
				return s, fmt.Errorf("unexpected %q after label %q in %q", line[i], name, line)
			}
			if line[i-1] == '}' {
				break
			}
		}
	}

	if i >= len(line) || line[i] != ' ' {
		return s, fmt.Errorf("missing value in %q", line)
	}
	s.value = line[i+1:]
	if _, err := strconv.ParseFloat(s.value, 64); err != nil {
		return s, fmt.Errorf("value %q is not a number in %q", s.value, line)
	}
	return s, nil
}

// decodeLabelValue decodes a label value starting just after the opening quote and
// returns the decoded string plus the index just past the closing quote.
func decodeLabelValue(line string, pos int) (string, int, error) {
	var b strings.Builder
	for pos < len(line) {
		switch c := line[pos]; c {
		case '\\':
			if pos+1 >= len(line) {
				return "", 0, fmt.Errorf("dangling escape in %q", line)
			}
			switch line[pos+1] {
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case 'n':
				b.WriteByte('\n')
			default:
				return "", 0, fmt.Errorf("illegal escape %q in %q: the format defines only \\\\, \\\" and \\n",
					line[pos:pos+2], line)
			}
			pos += 2
		case '"':
			return b.String(), pos + 1, nil
		default:
			b.WriteByte(c)
			pos++
		}
	}
	return "", 0, fmt.Errorf("unterminated label value in %q", line)
}

// TestPrometheusEmptyCollector ensures a scrape before the first request is
// still a valid (if empty) payload.
func TestPrometheusEmptyCollector(t *testing.T) {
	out := NewCollector().Snapshot().Prometheus()
	if !strings.Contains(out, "# TYPE sap_proxy_requests_total counter") {
		t.Errorf("expected the metric headers even with no data, got:\n%s", out)
	}
	if strings.Contains(out, "sap_proxy_requests_total{") {
		t.Errorf("expected no request series before the first request, got:\n%s", out)
	}
}

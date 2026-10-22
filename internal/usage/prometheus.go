package usage

import (
	"fmt"
	"strconv"
	"strings"
)

// metricsContentType is the Prometheus text exposition format (version 0.0.4),
// which every scrape endpoint expects.
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// labelEscaper escapes label values for the Prometheus text exposition format.
//
// The format defines exactly three escapes — backslash, double quote and line
// feed — and a parser rejects anything else as an unknown escape sequence. So
// carriage return and tab are deliberately NOT escaped here: emitting `\r` or
// `\t` would be an illegal escape that makes the whole scrape fail, while the raw
// bytes are legal in a label value (label_value excludes only `"`, `\` and
// newline). This matches the reference implementation in client_golang's
// expfmt.text_escaper byte for byte.
//
// Model names are caller-controlled, so this is a real injection surface: an
// unescaped quote would corrupt every following series.
var labelEscaper = strings.NewReplacer(
	`\`, `\\`,
	`"`, `\"`,
	"\n", `\n`,
)

// Prometheus renders the snapshot in the Prometheus text exposition format.
//
// The exposition is hand-written on purpose: the alternative is pulling in
// client_golang, and the proxy has no third-party dependencies beyond gin and
// x/sync.
func (s Snapshot) Prometheus() string {
	var b strings.Builder

	writeMetric := func(name, help, metricType string, series func()) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, metricType)
		series()
	}

	writeCounter := func(name string, labels []label, value int64) {
		b.WriteString(name)
		b.WriteString(renderLabels(labels))
		b.WriteString(" ")
		b.WriteString(strconv.FormatInt(value, 10))
		b.WriteString("\n")
	}

	writeMetric(
		"sap_proxy_requests_total",
		"Proxied inference requests by surface, model, status and streaming mode. status is the upstream-effective status, not the status the client received: a streaming request whose upstream failed is recorded with the upstream error code (or 502) while the client still got 200, because the SSE headers are written before the upstream answer is known.",
		"counter",
		func() {
			for _, r := range s.Requests {
				writeCounter("sap_proxy_requests_total", []label{
					{"surface", r.Surface},
					{"model", r.Model},
					{"status", strconv.Itoa(r.Status)},
					{"stream", strconv.FormatBool(r.Stream)},
				}, r.Count)
			}
		},
	)

	tokenMetrics := []struct {
		name  string
		help  string
		value func(Tokens) int64
	}{
		{"sap_proxy_input_tokens_total", "Prompt tokens reported by the upstream.", func(t Tokens) int64 { return t.Input }},
		{"sap_proxy_output_tokens_total", "Completion tokens reported by the upstream.", func(t Tokens) int64 { return t.Output }},
		{"sap_proxy_cache_read_tokens_total", "Prompt tokens served from the provider prompt cache.", func(t Tokens) int64 { return t.CacheRead }},
		{"sap_proxy_cache_write_tokens_total", "Prompt tokens written to the provider prompt cache.", func(t Tokens) int64 { return t.CacheWrite }},
	}

	for _, m := range tokenMetrics {
		metric := m
		writeMetric(metric.name, metric.help, "counter", func() {
			for _, entry := range s.ByModel {
				writeCounter(metric.name, []label{
					{"surface", entry.Surface},
					{"model", entry.Model},
				}, metric.value(entry.Tokens))
			}
		})
	}

	writeMetric(
		"sap_proxy_usage_missing_total",
		"Successful requests whose response was inspected but carried no usage block. Counts only instrumented requests: an endpoint nobody instrumented shows up in sap_proxy_uninstrumented_total instead, and failed requests (status >= 400) are never counted here.",
		"counter",
		func() {
			for _, entry := range s.ByModel {
				writeCounter("sap_proxy_usage_missing_total", []label{
					{"surface", entry.Surface},
					{"model", entry.Model},
				}, entry.UsageMissing)
			}
		},
	)

	writeMetric(
		"sap_proxy_uninstrumented_total",
		"Successful requests that no observation path read, i.e. the endpoint has no accounting at all. Not the same as usage_missing: a non-zero value means a proxy code path is missing an Observe call, not that the upstream stayed silent.",
		"counter",
		func() {
			for _, entry := range s.ByModel {
				writeCounter("sap_proxy_uninstrumented_total", []label{
					{"surface", entry.Surface},
					{"model", entry.Model},
				}, entry.Uninstrumented)
			}
		},
	)

	writeMetric(
		"sap_proxy_usage_capture_dropped_total",
		"Requests whose usage existed but was discarded by the proxy while buffering the response (currently only oversized Gemini streams). These requests are also counted in sap_proxy_usage_missing_total: the number is unknown, but the loss is ours, not the upstream's.",
		"counter",
		func() {
			for _, entry := range s.ByModel {
				writeCounter("sap_proxy_usage_capture_dropped_total", []label{
					{"surface", entry.Surface},
					{"model", entry.Model},
				}, entry.UsageCaptureDropped)
			}
		},
	)

	writeMetric(
		"sap_proxy_request_duration_seconds",
		"Wall-clock duration of proxied requests, including streaming.",
		"summary",
		func() {
			for _, d := range s.Durations {
				labels := []label{{"surface", d.Surface}}
				fmt.Fprintf(&b, "sap_proxy_request_duration_seconds_sum%s %s\n",
					renderLabels(labels), formatFloat(d.SumSeconds))
				writeCounter("sap_proxy_request_duration_seconds_count", labels, d.Count)
			}
		},
	)

	writeMetric(
		"sap_proxy_uptime_seconds",
		"Seconds since the proxy started collecting usage.",
		"gauge",
		func() {
			fmt.Fprintf(&b, "sap_proxy_uptime_seconds %s\n", formatFloat(s.UptimeSeconds))
		},
	)

	return b.String()
}

// label is one label name/value pair.
type label struct {
	name  string
	value string
}

// renderLabels renders labels in the form {name="value",...}. No labels renders
// as an empty string, which is valid for an unlabelled series.
func renderLabels(labels []label) string {
	if len(labels) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("{")
	for i, l := range labels {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(l.name)
		b.WriteString(`="`)
		b.WriteString(labelEscaper.Replace(l.value))
		b.WriteString(`"`)
	}
	b.WriteString("}")
	return b.String()
}

// formatFloat renders a float without an exponent for the magnitudes we expect
// and without a trailing ".0" for whole numbers, keeping the payload readable.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

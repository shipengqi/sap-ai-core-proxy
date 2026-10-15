package catalogue

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Requested Claude 4.x models (dash and dot forms).
		{"sonnet 4.6 dash", "claude-sonnet-4-6", "anthropic--claude-4.6-sonnet"},
		{"sonnet 4.6 dot", "claude-sonnet-4.6", "anthropic--claude-4.6-sonnet"},
		{"opus 4.8 dash", "claude-opus-4-8", "anthropic--claude-4.8-opus"},
		{"opus 4.8 dot", "claude-opus-4.8", "anthropic--claude-4.8-opus"},
		{"haiku 4.5 dash", "claude-haiku-4-5", "anthropic--claude-4.5-haiku"},
		{"haiku 4.5 dot", "claude-haiku-4.5", "anthropic--claude-4.5-haiku"},

		// Previously aliased models must keep working.
		{"sonnet 4.5 dash", "claude-sonnet-4-5", "anthropic--claude-4.5-sonnet"},
		{"sonnet 4.5 snapshot", "claude-sonnet-4-5-20251022", "anthropic--claude-4.5-sonnet"},
		{"sonnet latest", "claude-sonnet-latest", "anthropic--claude-4.5-sonnet"},
		{"opus latest", "claude-opus-latest", "anthropic--claude-4.7-opus"},
		{"opus 4.5", "claude-opus-4.5", "anthropic--claude-4.5-opus"},

		// Newly released versions resolve without an alias entry.
		{"sonnet 4.9 no alias", "claude-sonnet-4-9", "anthropic--claude-4.9-sonnet"},
		{"sonnet 4.6 snapshot", "claude-sonnet-4-6-20260115", "anthropic--claude-4.6-sonnet"},

		// Legacy 3.x naming conventions.
		{"3.7 sonnet latest", "claude-3-7-sonnet-latest", "anthropic--claude-3.7-sonnet"},
		{"3.7 sonnet snapshot", "claude-3-7-sonnet-20250219", "anthropic--claude-3.7-sonnet"},
		{"3.5 sonnet latest", "claude-3-5-sonnet-latest", "anthropic--claude-3.5-sonnet"},
		{"3.5 haiku latest", "claude-3-5-haiku-latest", "anthropic--claude-3.5-haiku"},
		{"3 opus latest", "claude-3-opus-latest", "anthropic--claude-3-opus"},
		{"3 sonnet snapshot", "claude-3-sonnet-20240229", "anthropic--claude-3-sonnet"},

		// Already in SAP form, and non-Claude names, pass through untouched.
		{"sap form untouched", "anthropic--claude-4.6-sonnet", "anthropic--claude-4.6-sonnet"},
		{"gpt untouched", "gpt-4o", "gpt-4o"},
		{"gemini untouched", "gemini-1.5-pro", "gemini-1.5-pro"},
		{"unknown claude untouched", "claude-something-weird", "claude-something-weird"},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in); got != tc.want {
				t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalize_CaseAndWhitespace(t *testing.T) {
	for _, in := range []string{"  Claude-Sonnet-4-6  ", "CLAUDE-SONNET-4-6", "claude-sonnet-4-6"} {
		if got := Normalize(in); got != "anthropic--claude-4.6-sonnet" {
			t.Errorf("Normalize(%q) = %q, want anthropic--claude-4.6-sonnet", in, got)
		}
	}
}

func TestIsAnthropic(t *testing.T) {
	for _, in := range []string{
		"claude-sonnet-4-6", "claude-opus-4-8", "claude-haiku-4-5",
		"anthropic--claude-4.6-sonnet", "CLAUDE-3-5-SONNET-LATEST",
	} {
		if !IsAnthropic(in) {
			t.Errorf("IsAnthropic(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"gpt-4o", "gemini-1.5-pro", "sonar", "qwen-max"} {
		if IsAnthropic(in) {
			t.Errorf("IsAnthropic(%q) = true, want false", in)
		}
	}
}

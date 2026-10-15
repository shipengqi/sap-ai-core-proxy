package sapclient

import (
	"testing"
	"time"
)

func TestParseVersion(t *testing.T) {
	cases := map[string][]int{
		"3":   {3},
		"4.5": {4, 5},
		"4.8": {4, 8},
	}
	for in, want := range cases {
		got := parseVersion(in)
		if len(got) != len(want) {
			t.Fatalf("parseVersion(%q) = %v, want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("parseVersion(%q) = %v, want %v", in, got, want)
			}
		}
	}
}

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b []int
		want int
	}{
		{[]int{4, 8}, []int{4, 5}, 1},
		{[]int{4, 5}, []int{4, 8}, -1},
		{[]int{4, 5}, []int{4, 5}, 0},
		{[]int{4}, []int{4, 8}, -1}, // 4.0 < 4.8
		{[]int{4, 8}, []int{4}, 1},
		{[]int{3, 7}, []int{4}, -1},
	}
	for _, tc := range cases {
		if got := compareVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersion(%v, %v) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestNewestOfFamily(t *testing.T) {
	deps := []Deployment{
		{ID: "opus-45", ModelName: "anthropic--claude-4.5-opus", DeployedURL: "u-opus-45"},
		{ID: "opus-48", ModelName: "anthropic--claude-4.8-opus", DeployedURL: "u-opus-48"},
		{ID: "opus-3", ModelName: "anthropic--claude-3-opus", DeployedURL: "u-opus-3"},
		{ID: "sonnet-46", ModelName: "anthropic--claude-4.6-sonnet", DeployedURL: "u-sonnet-46"},
		{ID: "sonnet-35", ModelName: "anthropic--claude-3.5-sonnet", DeployedURL: "u-sonnet-35"},
		{ID: "haiku-45", ModelName: "anthropic--claude-4.5-haiku", DeployedURL: "u-haiku-45"},
		{ID: "gpt", ModelName: "gpt-4o", DeployedURL: "u-gpt"},
	}
	d := &DeploymentManager{blacklist: map[string]time.Time{}}

	want := map[string]string{"opus": "opus-48", "sonnet": "sonnet-46", "haiku": "haiku-45"}
	for family, wantID := range want {
		got := d.newestOfFamily(deps, family)
		if got == nil {
			t.Fatalf("newestOfFamily(%q) = nil, want %s", family, wantID)
		}
		if got.ID != wantID {
			t.Errorf("newestOfFamily(%q) = %s, want %s", family, got.ID, wantID)
		}
	}

	if got := d.newestOfFamily(deps, "llama"); got != nil {
		t.Errorf("newestOfFamily(llama) = %v, want nil", got)
	}

	// A blacklisted deployment must be skipped, so the next-newest wins.
	d.MarkUnhealthy("u-opus-48")
	if got := d.newestOfFamily(deps, "opus"); got == nil || got.ID != "opus-45" {
		t.Errorf("after blacklisting the newest, newestOfFamily(opus) = %v, want opus-45", got)
	}
}

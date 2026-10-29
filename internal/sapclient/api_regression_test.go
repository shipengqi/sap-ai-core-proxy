package sapclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProxyRegressionModelIdentity(t *testing.T) {
	d := NewDeploymentManager(nil)
	d.cached = []Deployment{{ID: "4o", ModelName: "gpt-4o", DeployedURL: "http://unused"}}
	d.fetchedAt = time.Now()
	dep, err := d.GetDeployment(context.Background(), "gpt-6.1-sol")
	if err == nil {
		t.Fatalf("request for gpt-6.1-sol silently selected %s", dep.EffectiveModelName())
	}
}

func TestLatestFailoverPinsModel(t *testing.T) {
	d := NewDeploymentManager(nil)
	d.cached = []Deployment{{ID: "new", ModelName: "anthropic--claude-4.8-opus", DeployedURL: "http://new"}, {ID: "old", ModelName: "anthropic--claude-4.5-opus", DeployedURL: "http://old"}}
	d.fetchedAt = time.Now()
	calls := 0
	_, _, err := d.FindAndCall(t.Context(), "claude-opus-latest", 5, func(dep *Deployment) (int, []byte, error) {
		calls++
		if dep.ID != "new" {
			t.Fatal("latest request retried on older model:", dep.ID)
		}
		return 410, []byte(`{"error":"Gone"}`), nil
	})
	if err == nil || calls != 1 {
		t.Fatal("expected exhaustion of the original model", calls, err)
	}
}

func TestStreamingFailoverPreservesSameModelAndErrorBody(t *testing.T) {
	for _, status := range []int{410, 404, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			d := NewDeploymentManager(nil)
			d.cached = []Deployment{{ID: "a", ModelName: "gpt-6.1-sol", DeployedURL: "http://a"}, {ID: "b", ModelName: "gpt-6.1-sol", DeployedURL: "http://b"}, {ID: "wrong", ModelName: "gpt-4o", DeployedURL: "http://wrong"}}
			d.fetchedAt = time.Now()
			calls := 0
			resp, dep, err := d.CallStreaming(t.Context(), &d.cached[0], 5, func(dep *Deployment) (*http.Response, error) {
				calls++
				if dep.ID == "wrong" {
					t.Fatal("model changed")
				}
				code, body := status, `{"error":{"message":"endpoint unavailable"}}`
				if dep.ID == "b" {
					code, body = 200, "data: [DONE]\n\n"
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if status == 410 {
				if calls != 2 || dep.ID != "b" || resp.StatusCode != 200 {
					t.Fatal(calls, dep.ID, resp.StatusCode)
				}
			} else if calls != 1 || resp.StatusCode != status || !strings.Contains(string(body), "endpoint unavailable") {
				t.Fatal(calls, resp.StatusCode, string(body))
			}
		})
	}
}
func TestProxyRegressionSuccessfulGoneContent(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"Gone"},"finish_reason":"stop"}]}`)
	if isRetryableStatus(200, body) {
		t.Fatal("successful model output Gone triggers blacklist and retry")
	}
}

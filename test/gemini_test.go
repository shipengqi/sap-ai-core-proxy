package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

func setupGemini(t *testing.T) (proxyURL string, cleanup func()) {
	t.Helper()
	_, _, _, _, hasReal := testutil.RealCreds()
	if hasReal {
		proxy, err := testutil.NewTestProxy("")
		if err != nil {
			t.Fatalf("start real proxy: %v", err)
		}
		return proxy.URL, proxy.Close
	}
	mock := testutil.NewMockSAPServer(t)
	proxy, err := testutil.NewTestProxy(mock.URL)
	if err != nil {
		t.Fatalf("start mock proxy: %v", err)
	}
	return proxy.URL, func() {
		proxy.Close()
		mock.Close()
	}
}

func TestGemini_ListModels(t *testing.T) {
	proxyURL, cleanup := setupGemini(t)
	defer cleanup()

	resp, err := http.Get(proxyURL + "/gemini/v1/models")
	if err != nil {
		t.Fatalf("GET /gemini/v1/models: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Models []interface{} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Models) == 0 {
		t.Error("expected non-empty models list")
	}
}

func TestGemini_GenerateContent(t *testing.T) {
	proxyURL, cleanup := setupGemini(t)
	defer cleanup()

	reqBody := `{"contents":[{"role":"user","parts":[{"text":"Say hi"}]}]}`
	resp, err := http.Post(
		proxyURL+"/gemini/v1beta/models/gemini-1.5-pro:generateContent",
		"application/json",
		strings.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("POST generateContent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		t.Error("expected non-empty candidates[0].content.parts")
	}
}

func TestGemini_StreamGenerateContent(t *testing.T) {
	proxyURL, cleanup := setupGemini(t)
	defer cleanup()

	reqBody := `{"contents":[{"role":"user","parts":[{"text":"Say hi"}]}]}`
	resp, err := http.Post(
		proxyURL+"/gemini/v1beta/models/gemini-1.5-pro:streamGenerateContent",
		"application/json",
		strings.NewReader(reqBody),
	)
	if err != nil {
		t.Fatalf("POST streamGenerateContent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream body: %v", err)
	}
	bodyStr := string(body)

	// Gemini streaming is raw chunked JSON — should contain candidates
	if !strings.Contains(bodyStr, "candidates") {
		t.Errorf("expected 'candidates' in Gemini stream response, got: %q", bodyStr)
	}
}

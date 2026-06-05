package test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipengqi/sap-ai-core-proxy/test/testutil"
)

func setupOpenAI(t *testing.T) (proxyURL string, cleanup func()) {
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

func TestOpenAI_ListModels(t *testing.T) {
	proxyURL, cleanup := setupOpenAI(t)
	defer cleanup()

	resp, err := http.Get(proxyURL + "/openai/v1/models")
	if err != nil {
		t.Fatalf("GET /openai/v1/models: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Object string        `json:"object"`
		Data   []interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Object != "list" {
		t.Errorf("expected object=list, got %q", body.Object)
	}
	if len(body.Data) == 0 {
		t.Error("expected non-empty model list")
	}
}

func TestOpenAI_ChatCompletions(t *testing.T) {
	proxyURL, cleanup := setupOpenAI(t)
	defer cleanup()

	model := "gpt-4o"
	reqBody := `{"model":"` + model + `","messages":[{"role":"user","content":"Say hi"}]}`
	resp, err := http.Post(proxyURL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
		t.Error("expected non-empty choices[0].message.content")
	}
}

func TestOpenAI_ChatCompletions_Stream(t *testing.T) {
	proxyURL, cleanup := setupOpenAI(t)
	defer cleanup()

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"Say hi"}],"stream":true}`
	resp, err := http.Post(proxyURL+"/openai/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/chat/completions stream: %v", err)
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

	if !strings.Contains(bodyStr, "data: ") {
		t.Errorf("expected SSE data: lines in stream response, got: %q", bodyStr)
	}
	if !strings.Contains(bodyStr, "[DONE]") {
		t.Errorf("expected [DONE] in stream response")
	}
}

func TestOpenAI_Embeddings(t *testing.T) {
	proxyURL, cleanup := setupOpenAI(t)
	defer cleanup()

	reqBody := `{"model":"gpt-4o","input":"Hello world"}`
	resp, err := http.Post(proxyURL+"/openai/v1/embeddings", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/embeddings: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		t.Error("expected non-empty data[0].embedding")
	}
}

func TestOpenAI_Responses_CRUD(t *testing.T) {
	proxyURL, cleanup := setupOpenAI(t)
	defer cleanup()

	// POST — create
	createBody := `{"model":"gpt-4o","input":"Hello"}`
	resp, err := http.Post(proxyURL+"/openai/v1/responses", "application/json", strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("POST /openai/v1/responses: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create response: expected 200/201, got %d: %s", resp.StatusCode, body)
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty response ID")
	}

	// GET
	getResp, err := http.Get(proxyURL + "/openai/v1/responses/" + created.ID)
	if err != nil {
		t.Fatalf("GET /openai/v1/responses/%s: %v", created.ID, err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("GET response: expected 200, got %d", getResp.StatusCode)
	}

	// DELETE
	req, _ := http.NewRequest(http.MethodDelete, proxyURL+"/openai/v1/responses/"+created.ID, nil)
	delResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /openai/v1/responses/%s: %v", created.ID, err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK && delResp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE response: expected 200/204, got %d", delResp.StatusCode)
	}
}

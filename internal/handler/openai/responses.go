package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

)

var (
	responseStore   = map[string]string{} // responseID → deploymentURL
	responseRGStore = map[string]string{} // responseID → resourceGroupID
	responseStoreMu sync.Mutex
)

// CreateResponse proxies POST /openai/v1/responses and records the deployment URL.
func (h *Handler) CreateResponse(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorBody("read request body: "+err.Error()))
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, errorBody("invalid JSON: "+err.Error()))
		return
	}

	modelName := req.Model
	dep, err := h.deployments.GetDeployment(c.Request.Context(), modelName)
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	upstream, err := h.client.Do(c.Request.Context(), http.MethodPost,
		dep.DeployedURL+"/responses?api-version=2024-02-01", bytes.NewReader(body), nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer upstream.Body.Close()
	respBody, _ := io.ReadAll(upstream.Body)

	if upstream.StatusCode == http.StatusOK || upstream.StatusCode == http.StatusCreated {
		var resp struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(respBody, &resp); err == nil && resp.ID != "" {
			responseStoreMu.Lock()
			responseStore[resp.ID] = dep.DeployedURL
			responseRGStore[resp.ID] = dep.ResourceGroupID
			responseStoreMu.Unlock()
		}
	}

	c.Data(upstream.StatusCode, "application/json", respBody)
}

// GetResponse proxies GET /openai/v1/responses/:id.
func (h *Handler) GetResponse(c *gin.Context) {
	id := c.Param("id")
	deployURL, rg := lookupResponse(id)
	if deployURL == "" {
		c.JSON(http.StatusNotFound, errorBody("response not found: "+id))
		return
	}

	upstream, err := h.client.Do(c.Request.Context(), http.MethodGet,
		deployURL+"/responses/"+id, nil, rgHeaders(rg))
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer upstream.Body.Close()
	respBody, _ := io.ReadAll(upstream.Body)
	c.Data(upstream.StatusCode, "application/json", respBody)
}

// DeleteResponse proxies DELETE /openai/v1/responses/:id.
func (h *Handler) DeleteResponse(c *gin.Context) {
	id := c.Param("id")
	deployURL, rg := lookupResponse(id)
	if deployURL == "" {
		c.JSON(http.StatusNotFound, errorBody("response not found: "+id))
		return
	}

	upstream, err := h.client.Do(c.Request.Context(), http.MethodDelete,
		deployURL+"/responses/"+id, nil, rgHeaders(rg))
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer upstream.Body.Close()
	respBody, _ := io.ReadAll(upstream.Body)

	responseStoreMu.Lock()
	delete(responseStore, id)
	delete(responseRGStore, id)
	responseStoreMu.Unlock()

	c.Data(upstream.StatusCode, "application/json", respBody)
}

func lookupResponse(id string) (deployURL, rg string) {
	responseStoreMu.Lock()
	defer responseStoreMu.Unlock()
	return responseStore[id], responseRGStore[id]
}

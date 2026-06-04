package provider

import (
	"github.com/gin-gonic/gin"

	"github.com/shipengqi/sap-ai-core-proxy/internal/catalogue"
)

// ClaudeDispatcher routes by SAP model name to the appropriate handler.
type ClaudeDispatcher struct {
	converseHandler func(*gin.Context)
	invokeHandler   func(*gin.Context)
}

func NewClaudeDispatcher(converse, invoke func(*gin.Context)) *ClaudeDispatcher {
	return &ClaudeDispatcher{converseHandler: converse, invokeHandler: invoke}
}

func (d *ClaudeDispatcher) Dispatch(sapModelName string, c *gin.Context) {
	if catalogue.UsesConverseAPI(sapModelName) {
		d.converseHandler(c)
	} else {
		d.invokeHandler(c)
	}
}

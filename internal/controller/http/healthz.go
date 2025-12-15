package http

import (
	"time"

	"github.com/gin-gonic/gin"

	apiv1 "github.com/shipengqi/sap-ai-core-proxy/pkg/api/proxy/v1"
	"github.com/shipengqi/sap-ai-core-proxy/pkg/core"
	"github.com/shipengqi/sap-ai-core-proxy/pkg/log"
)

func (c *Controller) Healthz(ctx *gin.Context) {
	log.W(ctx.Request.Context()).Infow("Healthz controller is called", "method", "Healthz", "status", "healthy")
	core.WriteResponse(ctx, apiv1.HealthzResponse{
		Status:    apiv1.ServiceStatus_Healthy,
		Timestamp: time.Now().Format(time.DateTime),
	}, nil)
}

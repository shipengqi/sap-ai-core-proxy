package grpc

import (
	"context"
	"time"

	emptypb "google.golang.org/protobuf/types/known/emptypb"

	apiv1 "github.com/shipengqi/sap-ai-core-proxy/pkg/api/proxy/v1"
	"github.com/shipengqi/sap-ai-core-proxy/pkg/log"
)

func (c *Controller) Healthz(ctx context.Context, rq *emptypb.Empty) (*apiv1.HealthzResponse, error) {
	log.W(ctx).Infow("Healthz handler is called", "method", "Healthz", "status", "healthy")
	return &apiv1.HealthzResponse{
		Status:    apiv1.ServiceStatus_Healthy,
		Timestamp: time.Now().Format(time.DateTime),
	}, nil
}

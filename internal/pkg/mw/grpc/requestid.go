package grpc

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/shipengqi/sap-ai-core-proxy/pkg/contextx"
	"github.com/shipengqi/sap-ai-core-proxy/pkg/errorsx"
	"github.com/shipengqi/sap-ai-core-proxy/pkg/known"
)

// RequestIDInterceptor is a gRPC interceptor used to set the request ID.
func RequestIDInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		var rid string
		md, _ := metadata.FromIncomingContext(ctx)

		// Retrieves the request ID from the request.
		if ids := md[known.XRequestID]; len(ids) > 0 {
			rid = ids[0]
		}

		// Generates a new UUID if no request ID is present.
		if rid == "" {
			rid = uuid.New().String()
			md.Append(known.XRequestID, rid)
		}

		// Sets the metadata into a new incoming context.
		ctx = metadata.NewIncomingContext(ctx, md)

		// Sets the request ID into the response header metadata.
		// grpc.SetHeader adds metadata to the gRPC method response.
		// In this case, it sets the metadata containing the request ID into
		// the response header.
		//
		// Note: grpc.SetHeader only sets the data;
		// it does not immediately send it to the client.
		// The Header Metadata is sent together with the RPC response when it is returned.
		_ = grpc.SetHeader(ctx, md)

		//nolint: staticcheck
		ctx = contextx.WithRequestID(ctx, rid)

		res, err := handler(ctx, req)
		if err != nil {
			return res, errorsx.FromError(err).WithRequestID(rid)
		}

		return res, nil
	}
}

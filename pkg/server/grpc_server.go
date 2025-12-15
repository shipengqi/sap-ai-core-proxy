package server

import (
	"context"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/shipengqi/sap-ai-core-proxy/pkg/log"
	genericoptions "github.com/shipengqi/sap-ai-core-proxy/pkg/options"
)

// GRPCServer Represents a gRPC server.
type GRPCServer struct {
	srv *grpc.Server
	lis net.Listener
}

// NewGRPCServer Creates a new gRPC server instance.
func NewGRPCServer(
	grpcOptions *genericoptions.GRPCOptions,
	tlsOptions *genericoptions.TLSOptions,
	serverOptions []grpc.ServerOption,
	registerServer func(grpc.ServiceRegistrar),
) (*GRPCServer, error) {
	lis, err := net.Listen("tcp", grpcOptions.Addr)
	if err != nil {
		log.Errorw("Failed to listen", "err", err)
		return nil, err
	}

	if tlsOptions != nil && tlsOptions.UseTLS {
		tlsConfig := tlsOptions.MustTLSConfig()
		serverOptions = append(serverOptions, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}

	grpcsrv := grpc.NewServer(serverOptions...)

	registerServer(grpcsrv)
	registerHealthServer(grpcsrv)
	reflection.Register(grpcsrv)

	return &GRPCServer{
		srv: grpcsrv,
		lis: lis,
	}, nil
}

// RunOrDie Starts the gRPC server and logs a fatal error if it fails.
func (s *GRPCServer) RunOrDie() {
	log.Infow("Start to listening the incoming requests", "protocol", "grpc", "addr", s.lis.Addr().String())
	if err := s.srv.Serve(s.lis); err != nil {
		log.Fatalw("Failed to serve grpc server", "err", err)
	}
}

// GracefulStop Gracefully shuts down the gRPC server.
func (s *GRPCServer) GracefulStop(ctx context.Context) {
	log.Infow("Gracefully stop grpc server")
	s.srv.GracefulStop()
}

// registerHealthServer Registers the health check service.
func registerHealthServer(grpcsrv *grpc.Server) {
	// Creates a health check service instance.
	healthServer := health.NewServer()

	// Sets the health status of the service.
	healthServer.SetServingStatus("MiniBlog", grpc_health_v1.HealthCheckResponse_SERVING)

	// Registers the health check service.
	grpc_health_v1.RegisterHealthServer(grpcsrv, healthServer)
}

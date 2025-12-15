package server

import (
	"context"
	"net/http"
)

// Server define all the server types.
type Server interface {
	// RunOrDie Start the server, and exit if it fails to run.
	RunOrDie()
	// GracefulStop This method is used to gracefully shut down the server,
	// handling the context timeout during shutdown.
	GracefulStop(ctx context.Context)
}

// protocolName Get the protocol name from http.Server.
func protocolName(server *http.Server) string {
	if server.TLSConfig != nil {
		return "https"
	}
	return "http"
}

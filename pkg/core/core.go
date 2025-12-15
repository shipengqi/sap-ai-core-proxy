package core

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/shipengqi/sap-ai-core-proxy/pkg/errorsx"
)

// ErrorResponse defines the structure of an error response,
// used to return a unified, formatted error message when an
// error occurs during an API request.
type ErrorResponse struct {
	// Reason The reason for the error, indicating the type of error.
	Reason string `json:"reason,omitempty"`
	// Message error details
	Message string `json:"message,omitempty"`
	// Metadata Associated metadata information.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// WriteResponse is a generic response function.
// It generates either a success response or a standardized error response,
// depending on whether an error has occurred.
func WriteResponse(c *gin.Context, data any, err error) {
	if err != nil {
		errx := errorsx.FromError(err)
		c.JSON(errx.Code, ErrorResponse{
			Reason:   errx.Reason,
			Message:  errx.Message,
			Metadata: errx.Metadata,
		})
		return
	}
	c.JSON(http.StatusOK, data)
}

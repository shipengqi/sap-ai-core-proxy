package errorsx_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"

	"github.com/shipengqi/sap-ai-core-proxy/pkg/errorsx"
)

func TestErrorX_NewAndToString(t *testing.T) {
	errx := errorsx.New(500, "InternalError.DBConnection", "Database connection failed: %s", "timeout")

	assert.Equal(t, 500, errx.Code)
	assert.Equal(t, "InternalError.DBConnection", errx.Reason)
	assert.Equal(t, "Database connection failed: timeout", errx.Message)

	expected := `error: code = 500 reason = InternalError.DBConnection message = Database connection failed: timeout metadata = map[]`
	assert.Equal(t, expected, errx.Error())
}

func TestErrorX_WithMessage(t *testing.T) {
	errx := errorsx.New(400, "BadRequest.InvalidInput", "Invalid input for field %s", "username")

	errx.WithMessage("New error message: %s", "retry failed")

	assert.Equal(t, "New error message: retry failed", errx.Message)
	assert.Equal(t, 400, errx.Code)
	assert.Equal(t, "BadRequest.InvalidInput", errx.Reason)
}

func TestErrorX_WithMetadata(t *testing.T) {
	errx := errorsx.New(400, "BadRequest.InvalidInput", "Invalid input")

	errx.WithMetadata(map[string]string{
		"field": "username",
		"type":  "empty",
	})

	assert.Equal(t, "username", errx.Metadata["field"])
	assert.Equal(t, "empty", errx.Metadata["type"])

	errx.KV("user_id", "12345", "trace_id", "xyz-789")
	assert.Equal(t, "12345", errx.Metadata["user_id"])
	assert.Equal(t, "xyz-789", errx.Metadata["trace_id"])
}

func TestErrorX_Is(t *testing.T) {
	err1 := errorsx.New(404, "NotFound.User", "User not found")
	err2 := errorsx.New(404, "NotFound.User", "Another message")
	err3 := errorsx.New(403, "Forbidden", "Access denied")

	assert.True(t, err1.Is(err2))
	assert.False(t, err1.Is(err3))
}

func TestErrorX_FromError_WithGRPCError(t *testing.T) {
	grpcErr := status.New(3, "Invalid argument").Err() // gRPC INVALID_ARGUMENT = 3

	errx := errorsx.FromError(grpcErr)

	assert.Equal(t, 400, errx.Code)
	assert.Equal(t, "Invalid argument", errx.Message)

	assert.Nil(t, errx.Metadata)
}

func TestErrorX_FromError_WithGRPCErrorDetails(t *testing.T) {
	st := status.New(3, "Invalid argument")
	grpcErr, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   "InvalidInput",
		Metadata: map[string]string{"field": "name", "type": "required"},
	})
	assert.NoError(t, err)

	errx := errorsx.FromError(grpcErr.Err())

	assert.Equal(t, 400, errx.Code) // gRPC INVALID_ARGUMENT = HTTP 400
	assert.Equal(t, "Invalid argument", errx.Message)
	assert.Equal(t, "InvalidInput", errx.Reason)

	assert.Equal(t, "name", errx.Metadata["field"])
	assert.Equal(t, "required", errx.Metadata["type"])
}

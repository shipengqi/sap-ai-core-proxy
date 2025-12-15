package errorsx

import (
	"errors"
	"fmt"
	"net/http"

	httpstatus "github.com/go-kratos/kratos/v2/transport/http/status"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
)

type ErrorX struct {
	// Code Represents the HTTP status code of the error,
	// used to indicate the type of error when interacting with the client.
	Code int `json:"code,omitempty"`

	// Reason Represents the reason for the error, typically a business error
	// code used for precise issue identification.
	Reason string `json:"reason,omitempty"`

	// Message Represents a short error message that can typically be safely
	// exposed to the user.
	Message string `json:"message,omitempty"`

	// Metadata Used to store additional metadata related to the error,
	// which may include context or debugging information.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// New Creates a new ErrorX instance.
func New(code int, reason string, format string, args ...any) *ErrorX {
	return &ErrorX{
		Code:    code,
		Reason:  reason,
		Message: fmt.Sprintf(format, args...),
	}
}

// Error implements the `Error` method from the `error` interface.
func (err *ErrorX) Error() string {
	return fmt.Sprintf("error: code = %d reason = %s message = %s metadata = %v", err.Code, err.Reason, err.Message, err.Metadata)
}

// WithMessage Sets the message field of the ErrorX.
func (err *ErrorX) WithMessage(format string, args ...any) *ErrorX {
	err.Message = fmt.Sprintf(format, args...)
	return err
}

// WithMetadata Sets the metadata.
func (err *ErrorX) WithMetadata(md map[string]string) *ErrorX {
	err.Metadata = md
	return err
}

// KV Sets the metadata with the key-value pairs.
func (err *ErrorX) KV(kvs ...string) *ErrorX {
	if err.Metadata == nil {
		err.Metadata = make(map[string]string)
	}

	for i := 0; i < len(kvs); i += 2 {
		if i+1 < len(kvs) {
			err.Metadata[kvs[i]] = kvs[i+1]
		}
	}
	return err
}

// GRPCStatus return gRPC status.
func (err *ErrorX) GRPCStatus() *status.Status {
	details := errdetails.ErrorInfo{Reason: err.Reason, Metadata: err.Metadata}
	s, _ := status.New(httpstatus.ToGRPCCode(err.Code), err.Message).WithDetails(&details)
	return s
}

// WithRequestID Sets the request ID.
func (err *ErrorX) WithRequestID(requestID string) *ErrorX {
	return err.KV("X-Request-ID", requestID)
}

// Is reports whether any error in err's chain matches target.
// Returns true if both the Code and Reason are equal; otherwise, returns false.
func (err *ErrorX) Is(target error) bool {
	if errx := new(ErrorX); errors.As(target, &errx) {
		return errx.Code == err.Code && errx.Reason == err.Reason
	}
	return false
}

// Code return the HTTP Code of the error.
func Code(err error) int {
	if err == nil {
		return http.StatusOK //nolint:mnd
	}
	return FromError(err).Code
}

// Reason return the reason of the error.
func Reason(err error) string {
	if err == nil {
		return ErrInternal.Reason
	}
	return FromError(err).Reason
}

// FromError Attempts to convert a generic error into a custom *ErrorX type.
func FromError(err error) *ErrorX {
	if err == nil {
		return nil
	}

	// Checks whether the provided error is already an instance of the ErrorX type.
	// If the error can be converted to an *ErrorX type using errors.As, return directly.
	if errx := new(ErrorX); errors.As(err, &errx) {
		return errx
	}

	// The status.FromError method in gRPC attempts to convert an error into a gRPC status object.
	// If the error cannot be converted into a gRPC status error (i.e., it is not a gRPC status error),
	// an ErrorX with default values is returned, indicating an unknown type of error.
	gs, ok := status.FromError(err)
	if !ok {
		return New(ErrInternal.Code, ErrInternal.Reason, err.Error())
	}

	// If the error is of a gRPC error type, it will be successfully converted into a gRPC status object (`gs`).
	// Creates an ErrorX using the error code and message from the gRPC status.
	ret := New(httpstatus.FromGRPCCode(gs.Code()), ErrInternal.Reason, gs.Message())

	// Iterates through all additional information (Details) contained in the gRPC error.
	for _, detail := range gs.Details() {
		if typed, ok := detail.(*errdetails.ErrorInfo); ok {
			ret.Reason = typed.Reason
			return ret.WithMetadata(typed.Metadata)
		}
	}

	return ret
}

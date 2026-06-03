package sapclient

import "fmt"

// SapAPIError is returned when the SAP AI Core API responds with a 4xx status.
type SapAPIError struct {
	StatusCode int
	Message    string
}

func (e *SapAPIError) Error() string {
	return fmt.Sprintf("SAP API error %d: %s", e.StatusCode, e.Message)
}

func NewSapAPIError(statusCode int, message string) *SapAPIError {
	return &SapAPIError{StatusCode: statusCode, Message: message}
}

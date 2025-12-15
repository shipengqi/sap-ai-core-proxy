package known

// Defines HTTP/gRPC headers.
// gRPC is based on the HTTP/2, and according to the HTTP/2 specification,
// all header keys must be lowercase. Therefore, in gRPC, all header keys
// are automatically converted to lowercase to comply with the HTTP/2 requirements.
//
// Some HTTP frameworks or utility libraries (such as certain web servers or proxies) may
// automatically convert headers to lowercase to simplify processing logic.
// For compatibility reasons, all headers are standardized to lowercase.
// Additionally, header keys prefixed with x- indicate that they are custom headers.
const (
	// XRequestID is used to define the key in the context that represents the request ID.
	XRequestID = "x-request-id"

	// XUserID is used to define the key in the context that represents user ID.
	XUserID = "x-user-id"

	//  XUserID is used to define the key in the context that represents username.
	XUsername = "x-username"
)

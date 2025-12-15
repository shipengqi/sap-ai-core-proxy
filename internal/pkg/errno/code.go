package errno

import (
	"net/http"

	"github.com/shipengqi/sap-ai-core-proxy/pkg/errorsx"
)

var (
	OK                  = errorsx.OK
	ErrInternal         = errorsx.ErrInternal
	ErrNotFound         = errorsx.ErrNotFound
	ErrBind             = errorsx.ErrBind
	ErrInvalidArgument  = errorsx.ErrInvalidArgument
	ErrUnauthenticated  = errorsx.ErrUnauthenticated
	ErrPermissionDenied = errorsx.ErrPermissionDenied
	ErrOperationFailed  = errorsx.ErrOperationFailed

	// ErrPageNotFound Indicates that the page was not found.
	ErrPageNotFound = &errorsx.ErrorX{Code: http.StatusNotFound, Reason: "NotFound.PageNotFound", Message: "Page not found."}

	// ErrSignToken Indicates an error occurred while issuing the JWT token.
	ErrSignToken = &errorsx.ErrorX{Code: http.StatusUnauthorized, Reason: "Unauthenticated.SignToken", Message: "Error occurred while signing the JSON web token."}

	// ErrTokenInvalid Indicates that the JWT token format is invalid.
	ErrTokenInvalid = &errorsx.ErrorX{Code: http.StatusUnauthorized, Reason: "Unauthenticated.TokenInvalid", Message: "Token was invalid."}

	// ErrDBRead Indicates a database read failure.
	ErrDBRead = &errorsx.ErrorX{Code: http.StatusInternalServerError, Reason: "InternalError.DBRead", Message: "Database read failure."}

	// ErrDBWrite Indicates a database write failure.
	ErrDBWrite = &errorsx.ErrorX{Code: http.StatusInternalServerError, Reason: "InternalError.DBWrite", Message: "Database write failure."}

	// ErrAddRole Indicates an error occurred while adding a role.
	ErrAddRole = &errorsx.ErrorX{Code: http.StatusInternalServerError, Reason: "InternalError.AddRole", Message: "Error occurred while adding the role."}

	// ErrRemoveRole Indicates an error occurred while deleting a role.
	ErrRemoveRole = &errorsx.ErrorX{Code: http.StatusInternalServerError, Reason: "InternalError.RemoveRole", Message: "Error occurred while removing the role."}
)

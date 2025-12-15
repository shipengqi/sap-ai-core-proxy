package errno

import (
	"net/http"

	"github.com/shipengqi/sap-ai-core-proxy/pkg/errorsx"
)

var (
	// ErrUsernameInvalid Indicates that the username is invalid.
	ErrUsernameInvalid = &errorsx.ErrorX{
		Code:    http.StatusBadRequest,
		Reason:  "InvalidArgument.UsernameInvalid",
		Message: "Invalid username: Username must consist of letters, digits, and underscores only, and its length must be between 3 and 20 characters.",
	}

	// ErrPasswordInvalid Indicates that the password is invalid.
	ErrPasswordInvalid = &errorsx.ErrorX{
		Code:    http.StatusBadRequest,
		Reason:  "InvalidArgument.PasswordInvalid",
		Message: "Password is incorrect.",
	}

	// ErrUserAlreadyExists Indicates that the user already exists.
	ErrUserAlreadyExists = &errorsx.ErrorX{Code: http.StatusBadRequest, Reason: "AlreadyExist.UserAlreadyExists", Message: "User already exists."}

	// ErrUserNotFound Indicates that the specified user was not found.
	ErrUserNotFound = &errorsx.ErrorX{Code: http.StatusNotFound, Reason: "NotFound.UserNotFound", Message: "User not found."}
)

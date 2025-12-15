/*
Package contextx provides extended functionality for contexts,
allowing user-related information—such as user ID, username,
and access token—to be stored and retrieved from the context.

The suffix "x" denotes an extension or variant, making the package name
concise and easy to remember. The functions in this package simplify passing
and managing user information within contexts, making it suitable for scenarios
where data needs to be propagated through the context.

Usage:

In middleware or service functions that handle HTTP requests, these methods can be used
to store user information in the context. This allows the data to be safely shared throughout
the entire request lifecycle, avoiding the need for global variables or passing data through
function parameters.

Example:

	ctx := context.Background()

	// Stores the user ID and username in the context.
	ctx = contextx.WithUserID(ctx, "user-xxxx")
	ctx = contextx.WithUsername(ctx, "sampleUser")

	// Extracts user information from the context.
	userID := contextx.UserID(ctx)
	username := contextx.Username(ctx)
*/
package contextx

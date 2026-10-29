package provider

import (
	"errors"
	"fmt"
)

// HTTPStatusError is the HTTP status of a failed provider call.
type HTTPStatusError struct {
	Code int
}

// Error returns the status as "HTTP <code>".
func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.Code)
}

// ShortCause returns a short cause for err: "HTTP <code>", "no
// connection", or "error". The cause goes into a chat reply, so it never
// carries the response body or the URL.
func ShortCause(err error) string {
	var se *HTTPStatusError
	if errors.As(err, &se) {
		return se.Error()
	}
	if errors.Is(err, ErrBeforeFirstByte) {
		return "no connection"
	}
	return "error"
}

package provider

import (
	"errors"
	"fmt"
	"strings"
)

// HTTPStatusError is the HTTP status of a failed provider call.
type HTTPStatusError struct {
	Code int
	// Body is the start of the response body on one line, with the API key
	// removed.
	Body string
}

// Error returns the status as "HTTP <code>".
func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.Code)
}

// effortWords are the words of an HTTP 400 body that show that the
// provider rejects the effort field of the request.
var effortWords = []string{"effort", "reasoning", "thinking"}

// RejectsEffort returns true when err is an HTTP 400 whose body contains,
// without regard to case, "effort", "reasoning", or "thinking". The
// caller then can send the call again with no effort field.
func RejectsEffort(err error) bool {
	var se *HTTPStatusError
	if !errors.As(err, &se) || se.Code != 400 {
		return false
	}
	body := strings.ToLower(se.Body)
	for _, w := range effortWords {
		if strings.Contains(body, w) {
			return true
		}
	}
	return false
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

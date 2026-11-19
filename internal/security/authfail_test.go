package security

import (
	"strings"
	"testing"
)

func TestClassifyFailure(t *testing.T) {
	for _, c := range []struct {
		name   string
		code   int
		output string
		want   FailureClass
	}{
		{"401", 1, "HTTPError: 401 Unauthorized from https://upload.pypi.org/legacy/", FailureInvalid},
		{"http 401", 1, "HTTP 401", FailureInvalid},
		{"status 401", 1, "error: status 401", FailureInvalid},
		{"status code 401", 1, "request failed, status code: 401", FailureInvalid},
		{"json 401", 1, `{"status": 401, "message": "Bad credentials"}`, FailureInvalid},
		{"403 with an auth word", 1, "HTTPError: 403 Forbidden\nInvalid or non-existent authentication information.", FailureInvalid},
		{"403 with bad credentials", 1, "HTTP/1.1 403\nBad credentials", FailureInvalid},
		{"403 without an auth word", 1, "HTTP 403: you do not have access to this repository", FailureNone},
		{"429 with invalid", 1, "HTTP 429 Too Many Requests: invalid request rate", FailureTransient},
		{"503", 1, "status code: 503 Service Unavailable", FailureTransient},
		{"network error", 1, "curl: (7) Failed to connect to upload.pypi.org port 443: Connection refused", FailureTransient},
		{"dns error", 1, "Could not resolve host: upload.pypi.org", FailureTransient},
		{"timeout before 401", 1, "read timed out\nHTTP 401", FailureTransient},
		{"exit code 0", 0, "HTTP 401 Unauthorized", FailureNone},
		{"plain failure", 1, "build failed: 2 errors", FailureNone},
		{"number that is not a status", 1, "processed 14012 files, 401 skipped", FailureNone},
		{"401 in the last 4 KB", 1, strings.Repeat("x", 5000) + "\nHTTP 401", FailureInvalid},
		{"401 before the last 4 KB", 1, "HTTP 401\n" + strings.Repeat("x", 5000), FailureNone},
	} {
		if got := ClassifyFailure(c.code, c.output); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

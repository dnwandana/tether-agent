package security

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// FailureClass is the kind of a failed command that got a bound secret.
type FailureClass int

// The kinds of a failure.
const (
	// FailureNone is not an auth failure.
	FailureNone FailureClass = iota
	// FailureTransient is a network, rate, or server failure. The secret can still be good.
	FailureTransient
	// FailureInvalid is a clear auth failure. The secret is probably bad.
	FailureInvalid
)

// failureTailBytes is the part of the output that ClassifyFailure reads.
const failureTailBytes = 4096

// statusForms find an HTTP status in the output. A bare number is not a status. The reason
// phrase form needs a capital letter, so "401 skipped" does not match.
var statusForms = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bHTTP(?:/[\d.]+)?\s+(\d{3})\b`),
	regexp.MustCompile(`\b(\d{3})\s+[A-Z][a-z]`),
	regexp.MustCompile(`(?i)\bstatus(?:[ _]code)?\s*[:=]?\s*(\d{3})\b`),
	regexp.MustCompile(`(?i)"status(?:_code)?"\s*:\s*(\d{3})\b`),
}

// These word lists are new text. They are lower case, and the match ignores case.
var (
	transientWords = []string{
		"timeout", "timed out", "rate limit", "too many requests", "connection refused",
		"connection reset", "could not resolve", "temporary failure", "temporarily unavailable",
		"service unavailable", "bad gateway",
	}
	authWords = []string{
		"invalid", "revoked", "expired", "bad credentials", "authentication", "unauthorized",
		"unauthenticated", "invalid_api_key", "invalid_token", "not authorized",
	}
)

// ClassifyFailure tells whether the output of a failed command shows an auth failure. It
// reads only a command with an exit code that is not 0, and only the last 4 KB of output.
func ClassifyFailure(exitCode int, output string) FailureClass {
	if exitCode == 0 {
		return FailureNone
	}
	if len(output) > failureTailBytes {
		output = output[len(output)-failureTailBytes:]
		for len(output) > 0 && !utf8.RuneStart(output[0]) {
			output = output[1:]
		}
	}
	lower := strings.ToLower(output)
	statuses := map[int]bool{}
	for _, re := range statusForms {
		for _, m := range re.FindAllStringSubmatch(output, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil {
				statuses[n] = true
			}
		}
	}
	for n := range statuses {
		if n == 408 || n == 425 || n == 429 || (n >= 500 && n <= 599) {
			return FailureTransient
		}
	}
	if hasAnyWord(lower, transientWords) {
		return FailureTransient
	}
	if statuses[401] || (statuses[403] && hasAnyWord(lower, authWords)) {
		return FailureInvalid
	}
	return FailureNone
}

// hasAnyWord reports whether text has one of words.
func hasAnyWord(text string, words []string) bool {
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

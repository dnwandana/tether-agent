package provider

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// Class is the action that the chain takes after a failed try of one entry.
type Class int

const (
	// ClassStop ends the call. No other try or entry can help.
	ClassStop Class = iota
	// ClassTimeout is a timeout of the response headers. The entry gets at most 2 tries.
	ClassTimeout
	// ClassQuota is a used-up quota. The chain skips the entry until the quota resets.
	ClassQuota
	// ClassNextEntry makes the chain go to the next entry.
	ClassNextEntry
	// ClassRetry makes the chain try the same entry again.
	ClassRetry
)

// quotaRetryAfter is the largest Retry-After wait that is not a used-up quota.
const quotaRetryAfter = 60 * time.Second

// The Z.AI business codes come from https://docs.z.ai/api-reference/api-code.
var (
	zaiQuotaCodes = map[string]bool{
		"1308": true, "1310": true, "1316": true, "1317": true, "1318": true,
		"1319": true, "1320": true, "1321": true,
	}
	zaiNextCodes = map[string]bool{
		"1113": true, "1309": true, "1311": true, "1313": true, "1314": true, "1315": true,
	}
	zaiRetryCodes = map[string]bool{
		"1302": true, "1305": true, "1234": true, "1200": true, "1230": true,
	}
)

// Classify returns the class of the error of one try of one entry. zai is true when the
// entry is a Z.AI entry. Only then does the business code of the body count.
func Classify(err error, zai bool) Class {
	if err == nil {
		return ClassStop
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ClassStop
	}
	if errors.Is(err, ErrHeaderTimeout) {
		return ClassTimeout
	}
	se, hasStatus := errors.AsType[*HTTPStatusError](err)
	code := ""
	if hasStatus && zai && (se.Code == 429 || (se.Code >= 500 && se.Code <= 599)) {
		code = se.ProviderCode
	}
	if zaiQuotaCodes[code] || (hasStatus && se.RetryAfter > quotaRetryAfter) {
		return ClassQuota
	}
	if hasStatus {
		switch se.Code {
		case 401, 402, 403:
			return ClassNextEntry
		case 400, 429:
			low := strings.ToLower(se.Body)
			for _, t := range billingTexts {
				if strings.Contains(low, t) {
					return ClassNextEntry
				}
			}
		}
	}
	if zaiNextCodes[code] {
		return ClassNextEntry
	}
	if errors.Is(err, ErrConnection) || zaiRetryCodes[code] {
		return ClassRetry
	}
	if hasStatus && (se.Code == 408 || se.Code == 429 || (se.Code >= 500 && se.Code <= 599)) {
		return ClassRetry
	}
	if errors.Is(err, ErrBeforeFirstByte) {
		return ClassNextEntry
	}
	return ClassStop
}

// ClassedError is the error of all tries of one entry, with its class.
type ClassedError struct {
	Entry string
	Class Class
	Err   error
}

// Error returns the text of the wrapped error.
func (e *ClassedError) Error() string { return e.Err.Error() }

// Unwrap returns the wrapped error.
func (e *ClassedError) Unwrap() error { return e.Err }

// ErrorClass returns the class of any error of Chain.Stream. It walks only to a
// ClassedError, so the multi-%w error of postJSON counts as one error.
func ErrorClass(err error) Class {
	if err == nil {
		return ClassStop
	}
	if ce, ok := err.(*ClassedError); ok {
		return ce.Class
	}
	if m, ok := err.(interface{ Unwrap() []error }); ok {
		if _, has := errors.AsType[*ClassedError](err); has {
			members := m.Unwrap()
			for _, e := range members {
				if c := ErrorClass(e); c == ClassRetry || c == ClassTimeout {
					return ClassRetry
				}
			}
			if len(members) > 0 {
				return ErrorClass(members[0])
			}
		}
	} else if u, ok := err.(interface{ Unwrap() error }); ok {
		if inner := u.Unwrap(); inner != nil {
			if _, has := errors.AsType[*ClassedError](inner); has {
				return ErrorClass(inner)
			}
		}
	}
	return Classify(err, false)
}

// Retryable returns true when the class of err is Retry or Timeout.
func Retryable(err error) bool {
	c := ErrorClass(err)
	return c == ClassRetry || c == ClassTimeout
}

// ZAIFlags returns whether the entry is a Z.AI entry, and whether it is a Z.AI Coding Plan
// entry.
func ZAIFlags(p config.Provider) (zai, codingPlan bool) {
	var host, path string
	if u, err := url.Parse(p.BaseURL); err == nil {
		host, path = strings.ToLower(u.Hostname()), strings.TrimRight(u.Path, "/")
	}
	zai = p.Preset == "zai" || p.Preset == "zai-coding" || host == "api.z.ai" || host == "open.bigmodel.cn"
	if !zai {
		return false, false
	}
	const coding = "/api/coding/paas/v4"
	codingPlan = p.Preset == "zai-coding" || path == coding || strings.HasPrefix(path, coding+"/")
	return zai, codingPlan
}

package provider

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// apiErr is an error of postJSON for a status that starts the fallback.
func apiErr(code int, body, providerCode string, retryAfter time.Duration) error {
	se := &HTTPStatusError{Code: code, Body: body, ProviderCode: providerCode, RetryAfter: retryAfter}
	return fmt.Errorf("provider p: %w: %w: %s", ErrBeforeFirstByte, se, body)
}

func TestClassify(t *testing.T) {
	headerTimeout := fmt.Errorf("provider p: %w: %w: %w: timeout awaiting response headers", ErrBeforeFirstByte, ErrConnection, ErrHeaderTimeout)
	cases := []struct {
		name string
		err  error
		zai  bool
		want Class
	}{
		{"cancel", fmt.Errorf("provider p: %w", context.Canceled), true, ClassStop},
		{"deadline", fmt.Errorf("provider p: %w", context.DeadlineExceeded), true, ClassStop},
		{"stop before timeout", errors.Join(context.Canceled, headerTimeout), true, ClassStop},
		{"header timeout", headerTimeout, true, ClassTimeout},
		{"zai 1308", apiErr(429, "limit", "1308", 0), true, ClassQuota},
		{"zai 1310", apiErr(429, "limit", "1310", 0), true, ClassQuota},
		{"zai 1316", apiErr(429, "limit", "1316", 0), true, ClassQuota},
		{"zai 1321", apiErr(429, "limit", "1321", 0), true, ClassQuota},
		{"zai 1308 on 503", apiErr(503, "limit", "1308", 0), true, ClassQuota},
		{"quota before billing", apiErr(429, "insufficient balance", "1308", 0), true, ClassQuota},
		{"retry-after 61 s", apiErr(429, "", "", 61*time.Second), false, ClassQuota},
		{"retry-after 60 s", apiErr(429, "", "", 60*time.Second), false, ClassRetry},
		{"401", apiErr(401, "bad key", "", 0), false, ClassNextEntry},
		{"402", apiErr(402, "", "", 0), false, ClassNextEntry},
		{"403", apiErr(403, "", "", 0), false, ClassNextEntry},
		{"400 billing", apiErr(400, `{"error":"Your credit balance is too low"}`, "", 0), false, ClassNextEntry},
		{"429 billing", apiErr(429, "INSUFFICIENT_QUOTA", "", 0), false, ClassNextEntry},
		{"zai 1113", apiErr(429, "", "1113", 0), true, ClassNextEntry},
		{"zai 1309", apiErr(429, "", "1309", 0), true, ClassNextEntry},
		{"zai 1311", apiErr(429, "", "1311", 0), true, ClassNextEntry},
		{"zai 1313", apiErr(429, "", "1313", 0), true, ClassNextEntry},
		{"zai 1315", apiErr(429, "", "1315", 0), true, ClassNextEntry},
		{"non-zai 1113", apiErr(429, "", "1113", 0), false, ClassRetry},
		{"connection", fmt.Errorf("provider p: %w: %w: dial tcp: connection refused", ErrBeforeFirstByte, ErrConnection), false, ClassRetry},
		{"408", apiErr(408, "", "", 0), false, ClassRetry},
		{"429", apiErr(429, "", "", 0), false, ClassRetry},
		{"500", apiErr(500, "", "", 0), false, ClassRetry},
		{"599", apiErr(599, "", "", 0), false, ClassRetry},
		{"zai 1302", apiErr(429, "", "1302", 0), true, ClassRetry},
		{"zai 1305", apiErr(429, "", "1305", 0), true, ClassRetry},
		{"zai 1312", apiErr(429, "", "1312", 0), true, ClassRetry},
		{"zai code on 400", fmt.Errorf("provider p: %w: x", &HTTPStatusError{Code: 400, ProviderCode: "1308"}), true, ClassStop},
		{"unusable", fmt.Errorf("provider x: %w: needs an API key", ErrBeforeFirstByte), false, ClassNextEntry},
		{"400", fmt.Errorf("provider p: %w: bad request", &HTTPStatusError{Code: 400, Body: "bad request"}), false, ClassStop},
		{"404", fmt.Errorf("provider p: %w: not found", &HTTPStatusError{Code: 404}), false, ClassStop},
		{"other", errors.New("boom"), false, ClassStop},
	}
	for _, c := range cases {
		if got := Classify(c.err, c.zai); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// A Z.AI code in the body of a non-Z.AI entry (an OpenRouter relay) must not start a quota skip.
func TestClassifyZAICodeOnRelay(t *testing.T) {
	err := apiErr(429, `{"error":{"code":"1308","message":"Usage limit reached"}}`, "1308", 0)
	if got := Classify(err, false); got != ClassRetry {
		t.Fatalf("got %d, want ClassRetry", got)
	}
	if got := Classify(err, true); got != ClassQuota {
		t.Fatalf("got %d, want ClassQuota", got)
	}
}

func TestClassedError(t *testing.T) {
	inner := apiErr(503, "down", "", 0)
	ce := &ClassedError{Entry: "primary", Class: ClassRetry, Err: inner}
	if ce.Error() != inner.Error() || !errors.Is(ce, ErrBeforeFirstByte) {
		t.Fatal(ce)
	}
	if se, ok := errors.AsType[*HTTPStatusError](ce); !ok || se.Code != 503 {
		t.Fatal(ce)
	}
}

func TestErrorClass(t *testing.T) {
	classed := func(entry string, c Class) error {
		return &ClassedError{Entry: entry, Class: c, Err: apiErr(503, "x", "", 0)}
	}
	wrap := func(err error) error { return fmt.Errorf("agent: the model call to primary failed: %w", err) }
	quota, retry, timeout, next := classed("primary", ClassQuota), classed("fallback", ClassRetry),
		classed("fallback", ClassTimeout), classed("fallback", ClassNextEntry)
	cases := []struct {
		name string
		err  error
		want Class
	}{
		{"classed", quota, ClassQuota},
		{"wrapped classed", wrap(quota), ClassQuota},
		{"quota and retry", wrap(errors.Join(quota, retry)), ClassRetry},
		{"retry and quota", wrap(errors.Join(retry, quota)), ClassRetry},
		{"quota and timeout", wrap(errors.Join(quota, timeout)), ClassRetry},
		{"quota and next", wrap(errors.Join(quota, next)), ClassQuota},
		{"next and quota", wrap(errors.Join(next, quota)), ClassNextEntry},
		// A plain error of postJSON has more than one %w, but it is not a join of entries.
		{"plain 503", wrap(apiErr(503, "down", "", 0)), ClassRetry},
		{"plain 401", wrap(apiErr(401, "bad key", "", 0)), ClassNextEntry},
		{"plain retry-after 120 s", wrap(apiErr(429, "", "", 120*time.Second)), ClassQuota},
		// A plain error is never a Z.AI error.
		{"plain zai code", wrap(apiErr(429, "", "1308", 0)), ClassRetry},
		{"plain other", errors.New("boom"), ClassStop},
	}
	for _, c := range cases {
		if got := ErrorClass(c.err); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestRetryable(t *testing.T) {
	for c, want := range map[Class]bool{ClassRetry: true, ClassTimeout: true, ClassQuota: false, ClassNextEntry: false, ClassStop: false} {
		if got := Retryable(&ClassedError{Entry: "p", Class: c, Err: errors.New("x")}); got != want {
			t.Errorf("class %d: got %v, want %v", c, got, want)
		}
	}
	if !Retryable(fmt.Errorf("provider p: %w: %w: refused", ErrBeforeFirstByte, ErrConnection)) {
		t.Error("a connection error is not retryable")
	}
}

func TestZAIFlags(t *testing.T) {
	cases := []struct {
		p               config.Provider
		zai, codingPlan bool
	}{
		{config.Provider{Preset: "zai"}, true, false},
		{config.Provider{Preset: "zai-coding"}, true, true},
		{config.Provider{BaseURL: "https://api.z.ai/api/paas/v4"}, true, false},
		{config.Provider{BaseURL: "https://API.Z.AI/api/paas/v4"}, true, false},
		{config.Provider{BaseURL: "https://api.z.ai/api/coding/paas/v4/"}, true, true},
		{config.Provider{BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4"}, true, true},
		{config.Provider{BaseURL: "https://open.bigmodel.cn/api/paas/v4"}, true, false},
		{config.Provider{Preset: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}, false, false},
		{config.Provider{BaseURL: "https://api.z.ai.example.com/api/coding/paas/v4"}, false, false},
		{config.Provider{}, false, false},
	}
	for _, c := range cases {
		zai, cp := ZAIFlags(c.p)
		if zai != c.zai || cp != c.codingPlan {
			t.Errorf("%+v: got %v %v, want %v %v", c.p, zai, cp, c.zai, c.codingPlan)
		}
	}
}

func TestWithNotify(t *testing.T) {
	if notifyFrom(context.Background()) != nil {
		t.Fatal("a notify function with no WithNotify")
	}
	var got []Notice
	nctx := WithNotify(context.Background(), func(n Notice) { got = append(got, n) })
	fn := notifyFrom(nctx)
	if fn == nil {
		t.Fatal("no notify function")
	}
	fn(Notice{Kind: NoticeQuota, Entry: "primary"})
	if len(got) != 1 || got[0].Kind != NoticeQuota || got[0].Entry != "primary" {
		t.Fatal(got)
	}
	if notifyFrom(WithNotify(nctx, nil)) != nil {
		t.Fatal("a nil function does not clear")
	}
}

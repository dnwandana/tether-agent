package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// maxErrorBody is the number of bytes of an HTTP error body that goes into
// the error text.
const maxErrorBody = 512

// maxJSONBody is the byte limit of a JSON response body.
const maxJSONBody = 16 << 20

// finalSendWait is the time that an adapter waits to deliver the last
// event after the context is cancelled. It stops a goroutine leak when the
// caller does not read the channel any more.
const finalSendWait = 2 * time.Second

// billingTexts are the texts of an HTTP 400 body that show a billing or
// quota failure. Some providers send such a failure as a 400, not as a 402.
// The match is without regard to case.
var billingTexts = []string{"credit balance", "spend limit", "insufficient balance", "insufficient_quota", "arrearage"}

// postJSON sends body as JSON to url and returns the response when the
// status is 2xx. These failures wrap ErrBeforeFirstByte, so that the
// caller can try the next provider:
//   - a connection error
//   - an HTTP 5xx, 429, 401, 402, 403, or 408
//   - an HTTP 400 whose body tells of a billing or quota failure
//
// Another status gives an error with a short body, because a wrong request
// fails on each provider. An error for a bad status also wraps an
// HTTPStatusError that holds the short body. apiKey is removed from the
// error text and from the body.
func postJSON(ctx context.Context, hc *http.Client, name, url string, header http.Header, body any, apiKey string) (*http.Response, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("provider %s: cannot encode the request: %w", name, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("provider %s: bad request URL %q: %w", name, url, err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("provider %s: %w", name, ctx.Err())
		}
		return nil, fmt.Errorf("provider %s: %w: %s", name, ErrBeforeFirstByte, redact(err.Error(), apiKey))
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4*maxErrorBody))
	msg := shortBody(redact(string(b), apiKey))
	status := &HTTPStatusError{Code: resp.StatusCode, Body: msg}
	if triggersFallback(resp.StatusCode, string(b)) {
		return nil, fmt.Errorf("provider %s: %w: %w: %s", name, ErrBeforeFirstByte, status, msg)
	}
	return nil, fmt.Errorf("provider %s: %w: %s", name, status, msg)
}

// getJSON sends a GET to url and decodes the 2xx JSON body into out. A
// connection error wraps ErrBeforeFirstByte. A status that is not 2xx gives
// an error that wraps an HTTPStatusError with the short body. apiKey is
// removed from the error text and from the body.
func getJSON(ctx context.Context, hc *http.Client, name, url string, header http.Header, apiKey string, out any) error {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("provider %s: bad request URL %q: %w", name, redact(url, apiKey), err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("provider %s: %w", name, ctx.Err())
		}
		return fmt.Errorf("provider %s: %w: %s", name, ErrBeforeFirstByte, redact(err.Error(), apiKey))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4*maxErrorBody))
		msg := shortBody(redact(string(b), apiKey))
		status := &HTTPStatusError{Code: resp.StatusCode, Body: msg}
		return fmt.Errorf("provider %s: %w: %s", name, status, msg)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBody)).Decode(out); err != nil {
		return fmt.Errorf("provider %s: cannot decode the response: %w", name, err)
	}
	return nil
}

// triggersFallback returns true when an HTTP failure with code and body
// can succeed on another provider.
func triggersFallback(code int, body string) bool {
	switch {
	case code >= 500:
		return true
	case code == http.StatusTooManyRequests, code == http.StatusUnauthorized,
		code == http.StatusPaymentRequired, code == http.StatusForbidden,
		code == http.StatusRequestTimeout:
		return true
	case code == http.StatusBadRequest:
		lower := strings.ToLower(body)
		for _, t := range billingTexts {
			if strings.Contains(lower, t) {
				return true
			}
		}
	}
	return false
}

// redact removes the API key from s.
func redact(s, apiKey string) string {
	if len(apiKey) >= 4 {
		s = strings.ReplaceAll(s, apiKey, "[REDACTED]")
	}
	return s
}

// shortBody cuts s to maxErrorBody bytes on a rune border and removes
// line breaks, so that the error stays on one line.
func shortBody(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxErrorBody {
		return s
	}
	cut := maxErrorBody
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// emitter sends the events of one stream. It stops a send when the
// context is cancelled, so the goroutine never blocks for ever. It stamps
// each event with provider and model, so the caller knows which provider
// served the stream.
type emitter struct {
	ctx             context.Context
	ch              chan Event
	provider, model string
}

// send delivers ev. It returns false when the context is cancelled.
func (e *emitter) send(ev Event) bool {
	ev.Provider, ev.Model = e.provider, e.model
	select {
	case e.ch <- ev:
		return true
	case <-e.ctx.Done():
		return false
	}
}

// fail delivers the last event, an EventError. When the context is
// cancelled, the error is the context error. fail waits at most
// finalSendWait for a reader.
func (e *emitter) fail(err error) {
	if cerr := e.ctx.Err(); cerr != nil {
		err = cerr
	}
	t := time.NewTimer(finalSendWait)
	defer t.Stop()
	select {
	case e.ch <- Event{Type: EventError, Err: err, Provider: e.provider, Model: e.model}:
	case <-t.C:
	}
}

// errEndOfStream is the error for a stream that ends before its finish event.
func errEndOfStream(name string) error {
	return fmt.Errorf("provider %s: the stream ended before the finish event: %w", name, io.ErrUnexpectedEOF)
}

// imageData returns the bytes and the MIME type of img. It reads img.Path
// when img.Data is empty.
func imageData(img Image) ([]byte, string, error) {
	data := img.Data
	if len(data) == 0 && img.Path != "" {
		b, err := os.ReadFile(img.Path)
		if err != nil {
			return nil, "", fmt.Errorf("cannot read the image: %w", err)
		}
		data = b
	}
	if len(data) == 0 {
		return nil, "", errors.New("the image has no data")
	}
	mime := img.MIME
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return data, mime, nil
}

// b64 returns the standard base64 encoding of b.
func b64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// joinURL joins a base URL and a path with one slash.
func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

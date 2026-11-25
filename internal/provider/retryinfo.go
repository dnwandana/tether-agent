package provider

import (
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// HeaderTimeout is the time that the provider client waits for the response
// headers.
const HeaderTimeout = 300 * time.Second

// ErrHeaderTimeout shows that the server sent no response headers in time.
// postJSON wraps it with ErrBeforeFirstByte and ErrConnection.
var ErrHeaderTimeout = errors.New("provider sent no response headers in time")

// maxRetryAfter is the largest wait that parseRetryAfter returns.
const maxRetryAfter = 24 * time.Hour

// maxResetAhead is the largest distance to a reset time that parseResetAt accepts.
const maxResetAhead = 8 * 24 * time.Hour

// codeRE finds a four-digit business code in a body that is not valid JSON.
var codeRE = regexp.MustCompile(`"code"\s*:\s*"?(\d{4})(?:[^\d]|$)`)

// resetRE finds the text that comes after "reset at" or "resets at".
var resetRE = regexp.MustCompile(`(?i)resets? at\s+`)

// utc8 is the zone of a reset time that has no zone.
var utc8 = time.FixedZone("UTC+8", 8*60*60)

// zoneRE reads the zone after a time: UTC, GMT, or Z with an optional offset, or an offset
// with no name.
var zoneRE = regexp.MustCompile(`^(?:(?:UTC|GMT|Z)(?:([+-])(\d{1,2})(?::?(\d{2}))?)?|([+-])(\d{2}):?(\d{2}))(?:[^\w:]|$)`)

// zoneNameRE finds a zone name that zoneRE does not read, for example CST.
var zoneNameRE = regexp.MustCompile(`^[A-Z]{3,5}(?:[^\w]|$)`)

// zoneAfter returns the zone that s names at its start, after optional spaces. It returns
// UTC+8 when s names no zone. ok is false for a zone name that it cannot read.
func zoneAfter(s string) (loc *time.Location, ok bool) {
	s = strings.TrimLeft(s, " ")
	m := zoneRE.FindStringSubmatch(s)
	if m == nil {
		if zoneNameRE.MatchString(s) {
			return nil, false
		}
		return utc8, true
	}
	sign, hh, mm := m[1], m[2], m[3]
	if m[4] != "" {
		sign, hh, mm = m[4], m[5], m[6]
	}
	if sign == "" {
		return time.UTC, true
	}
	h, _ := strconv.Atoi(hh)
	mi, _ := strconv.Atoi(mm)
	if h > 14 || mi > 59 {
		return nil, false
	}
	off := (h*60 + mi) * 60
	if sign == "-" {
		off = -off
	}
	return time.FixedZone("", off), true
}

// isHeaderTimeout returns true when err is the timeout of the response
// headers of the HTTP transport.
func isHeaderTimeout(err error) bool {
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout() && strings.Contains(err.Error(), "timeout awaiting response headers")
}

// toDuration converts the number s of unit to a duration. It returns 0 for a bad, zero, or
// negative number. It clamps the number before the conversion, so a huge value cannot overflow.
func toDuration(s string, unit time.Duration) time.Duration {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0
	}
	if math.IsNaN(v) || v <= 0 {
		return 0
	}
	if v >= float64(maxRetryAfter/unit) {
		return maxRetryAfter
	}
	return time.Duration(v * float64(unit))
}

// parseRetryAfter returns the wait of the retry-after-ms or Retry-After
// header. It returns 0 when the header is absent, bad, zero, negative, or in
// the past. The result is at most maxRetryAfter.
func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	if d := toDuration(h.Get("retry-after-ms"), time.Millisecond); d > 0 {
		return d
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if d := toDuration(v, time.Second); d > 0 {
		return d
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	d := t.Sub(now)
	if d <= 0 {
		return 0
	}
	return min(d, maxRetryAfter)
}

// parseProviderCode returns the business code of an error body, or "". It
// reads error.code from JSON first. Else it searches the text, because the
// body can be cut.
func parseProviderCode(body []byte) string {
	var v struct {
		Error struct {
			Code json.RawMessage `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &v) == nil && len(v.Error.Code) > 0 {
		var s string
		if json.Unmarshal(v.Error.Code, &s) != nil {
			s = strings.TrimSpace(string(v.Error.Code))
		}
		if isDigits(s) {
			return s
		}
	}
	if m := codeRE.FindSubmatch(body); m != nil {
		return string(m[1])
	}
	return ""
}

// isDigits returns true when s is not empty and holds only ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseResetAt returns the reset time that msg names after "reset at", or
// the zero time. It also returns the zero time for a time that is not after
// now or is more than 8 days after now. A "2006-01-02 15:04:05" time uses the
// zone that follows it, else UTC+8. A zone name that it cannot read gives the
// zero time.
func parseResetAt(msg string, now time.Time) time.Time {
	loc := resetRE.FindStringIndex(msg)
	if loc == nil {
		return time.Time{}
	}
	rest := msg[loc[1]:]
	end := strings.IndexAny(rest, " \",;)")
	if end < 0 {
		end = len(rest)
	}
	token := strings.TrimRight(rest[:end], ".")
	var t time.Time
	if p, err := time.Parse(time.RFC3339, token); err == nil {
		t = p
	} else if len(rest) >= 19 {
		if zone, ok := zoneAfter(rest[19:]); ok {
			if p, err := time.ParseInLocation("2006-01-02 15:04:05", rest[:19], zone); err == nil {
				t = p
			}
		}
	}
	if t.IsZero() && isDigits(token) {
		n, err := strconv.ParseInt(token, 10, 64)
		if err != nil {
			return time.Time{}
		}
		if len(token) >= 13 {
			t = time.UnixMilli(n)
		} else {
			t = time.Unix(n, 0)
		}
	}
	if t.IsZero() || !t.After(now) || t.Sub(now) > maxResetAhead {
		return time.Time{}
	}
	return t
}

package telegram

import "encoding/json"

// maxRichDepth is the depth at which each conversion stops. Telegram allows
// 16 levels (Hermes _MAX_RICH_DEPTH).
const maxRichDepth = 64

// richIntLimit is the largest value that richInt returns.
const richIntLimit = 1 << 30

// richDoc is the parsed rich message of one inbound message. blocks is the
// "blocks" array, or nil. failed is true after a conversion panic.
type richDoc struct {
	blocks []any
	failed bool
}

// cacheRich stores the document of the message, so each later richOf call
// does no new parse.
func (m *Message) cacheRich() {
	m.rich = richOf(*m)
}

// richOf returns the rich document of the message. It returns nil when the
// rich content is not active.
func richOf(m Message) *richDoc {
	if len(m.RichMessage) == 0 || m.Text != "" || m.Caption != "" {
		return nil
	}
	if m.rich != nil {
		return m.rich
	}
	var v any
	if err := json.Unmarshal(m.RichMessage, &v); err != nil {
		return &richDoc{}
	}
	return &richDoc{blocks: richList(richMap(v)["blocks"])}
}

// Failed reports if a conversion of the document stopped with a panic.
func (d *richDoc) Failed() bool {
	return d.failed
}

// guard recovers a panic of a conversion. Use it as
// "defer d.guard(func() { out = "" })".
func (d *richDoc) guard(reset func()) {
	if r := recover(); r != nil {
		d.failed = true
		if reset != nil {
			reset()
		}
	}
}

// richMap returns v as an object, or nil.
func richMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// richList returns v as an array, or nil.
func richList(v any) []any {
	l, _ := v.([]any)
	return l
}

// richStr returns v as a string, or "".
func richStr(v any) string {
	s, _ := v.(string)
	return s
}

// richInt returns v as an integer, or 0. The result is in the range from
// -richIntLimit to richIntLimit.
func richInt(v any) int {
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	if f >= richIntLimit {
		return richIntLimit
	}
	if f <= -richIntLimit {
		return -richIntLimit
	}
	return int(f)
}

// richBool reports if v is the value true.
func richBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// richType returns the "type" string of an object, or "".
func richType(v any) string {
	return richStr(richMap(v)["type"])
}

// richObject copies the object v into the struct that out points to. It
// returns false when v is not an object or the copy fails.
func richObject(v any, out any) bool {
	m := richMap(v)
	if m == nil {
		return false
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

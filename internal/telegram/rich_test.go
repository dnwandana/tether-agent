package telegram

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// richMsg returns a message with the rich message {"blocks": blocksJSON} and
// no text.
func richMsg(blocksJSON string) Message {
	return Message{RichMessage: json.RawMessage(`{"blocks":` + blocksJSON + `}`)}
}

// richDocOf returns the document of richMsg(blocksJSON).
func richDocOf(t *testing.T, blocksJSON string) *richDoc {
	t.Helper()
	d := richOf(richMsg(blocksJSON))
	if d == nil {
		t.Fatalf("no rich document for %s", blocksJSON)
	}
	return d
}

// parseRichValue decodes a JSON text into the generic form that the converter
// reads.
func parseRichValue(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}

func TestRichActiveRule(t *testing.T) {
	const raw = `{"blocks":[{"type":"paragraph","text":"hello"}]}`
	cases := []struct {
		name   string
		msg    Message
		active bool
		blocks int
	}{
		{"rich only", Message{RichMessage: json.RawMessage(raw)}, true, 1},
		{"text set", Message{Text: "hi", RichMessage: json.RawMessage(raw)}, false, 0},
		{"caption set", Message{Caption: "hi", RichMessage: json.RawMessage(raw)}, false, 0},
		{"no field", Message{}, false, 0},
		{"empty raw", Message{RichMessage: json.RawMessage{}}, false, 0},
		{"null", Message{RichMessage: json.RawMessage(`null`)}, true, 0},
	}
	for _, c := range cases {
		d := richOf(c.msg)
		if (d != nil) != c.active {
			t.Errorf("%s: document %v, want active %v", c.name, d, c.active)
			continue
		}
		if d != nil && (len(d.blocks) != c.blocks || d.Failed()) {
			t.Errorf("%s: %d blocks, failed %v, want %d blocks", c.name, len(d.blocks), d.Failed(), c.blocks)
		}
	}
}

func TestRichWrongShapes(t *testing.T) {
	for _, raw := range []string{`5`, `"x"`, `[]`, `{"blocks":5}`, `{"blocks":"x"}`, `{"blocks":{}}`, `{}`, `{`} {
		d := richOf(Message{RichMessage: json.RawMessage(raw)})
		if d == nil {
			t.Errorf("%s: no document, want a document with no blocks", raw)
			continue
		}
		if len(d.blocks) != 0 || d.Failed() {
			t.Errorf("%s: %d blocks, failed %v", raw, len(d.blocks), d.Failed())
		}
	}
}

func TestRichOfDeepPayload(t *testing.T) {
	// The parse keeps each level. Each conversion stops at maxRichDepth.
	n := 4 * maxRichDepth
	d := richDocOf(t, "["+strings.Repeat(`{"type":"quote","blocks":[`, n)+strings.Repeat(`]}`, n)+"]")
	if len(d.blocks) != 1 || d.Failed() {
		t.Fatalf("%d blocks, failed %v, want 1 block", len(d.blocks), d.Failed())
	}
}

func TestRichOfUsesTheCache(t *testing.T) {
	m := richMsg(`[{"type":"paragraph","text":"a"}]`)
	cached := &richDoc{}
	m.rich = cached
	if got := richOf(m); got != cached {
		t.Fatalf("richOf = %p, want the cached document %p", got, cached)
	}
	m.Text = "typed text"
	if got := richOf(m); got != nil {
		t.Fatalf("richOf = %v, want nil when Text is set", got)
	}

	fresh := richMsg(`[{"type":"paragraph","text":"a"},{"type":"divider"}]`)
	fresh.cacheRich()
	if fresh.rich == nil || len(fresh.rich.blocks) != 2 {
		t.Fatalf("cacheRich: %+v", fresh.rich)
	}
	if richOf(fresh) != fresh.rich {
		t.Fatal("richOf must return the document that cacheRich stored")
	}
	plain := Message{Text: "hi"}
	plain.cacheRich()
	if plain.rich != nil {
		t.Fatalf("cacheRich on a plain message: %+v", plain.rich)
	}
}

func TestRichOfIgnoresTheRepliedToMessage(t *testing.T) {
	const raw = `{"message_id":7,"chat":{"id":5,"type":"private"},"text":"my answer",
		"reply_to_message":{"message_id":6,"chat":{"id":5,"type":"private"},
			"rich_message":{"blocks":[{"type":"paragraph","text":"the original @tether_bot"}]}}}`
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.ReplyToMessage == nil || len(m.ReplyToMessage.RichMessage) == 0 {
		t.Fatalf("the replied-to message lost its raw rich_message: %+v", m.ReplyToMessage)
	}
	if d := richOf(m); d != nil {
		t.Fatalf("richOf = %+v, want nil: the reply has its own text", d)
	}
	m.cacheRich()
	if m.rich != nil || m.ReplyToMessage.rich != nil {
		t.Fatal("cacheRich must not parse the rich_message of reply_to_message")
	}

	// A reply with no text and no rich_message of its own has no rich content.
	m.Text = ""
	if d := richOf(m); d != nil {
		t.Fatalf("richOf = %+v, want nil: only reply_to_message has a rich_message", d)
	}
}

func TestRichGuard(t *testing.T) {
	d := &richDoc{}
	out, calls := "", 0
	func() {
		defer d.guard(func() { out = ""; calls++ })
		out = "partial"
		panic("boom")
	}()
	if !d.Failed() || out != "" || calls != 1 {
		t.Fatalf("after a panic: failed %v, out %q, reset calls %d", d.Failed(), out, calls)
	}

	ok := &richDoc{}
	func() {
		defer ok.guard(func() { calls++ })
		out = "done"
	}()
	if ok.Failed() || out != "done" || calls != 1 {
		t.Fatalf("with no panic: failed %v, out %q, reset calls %d", ok.Failed(), out, calls)
	}
}

func TestRichAccessors(t *testing.T) {
	obj := parseRichValue(t, `{"type":"heading","size":2,"text":"T","is_open":true,"items":[1,"a"],"big":1e18,"small":-1e18,"frac":2.9}`)
	m := richMap(obj)
	if m == nil || richMap("x") != nil || richMap([]any{}) != nil || richMap(nil) != nil {
		t.Fatalf("richMap: %v", m)
	}
	if got := richList(m["items"]); len(got) != 2 || richList(m) != nil || richList("x") != nil || richList(nil) != nil {
		t.Fatalf("richList: %v", got)
	}
	if richStr(m["text"]) != "T" || richStr(m["size"]) != "" || richStr(nil) != "" || richStr(m) != "" {
		t.Fatal("richStr")
	}
	ints := []struct {
		v    any
		want int
	}{
		{m["size"], 2}, {m["frac"], 2}, {m["big"], 1073741824}, {m["small"], -1073741824},
		{m["text"], 0}, {m["is_open"], 0}, {nil, 0}, {m, 0},
	}
	for _, c := range ints {
		if got := richInt(c.v); got != c.want {
			t.Errorf("richInt(%v) = %d, want %d", c.v, got, c.want)
		}
	}
	if !richBool(m["is_open"]) || richBool(m["size"]) || richBool("true") || richBool(nil) || richBool(false) {
		t.Fatal("richBool")
	}
	if richType(obj) != "heading" || richType("heading") != "" || richType(nil) != "" ||
		richType(parseRichValue(t, `{"type":5}`)) != "" || richType(parseRichValue(t, `{"text":"x"}`)) != "" {
		t.Fatal("richType")
	}
}

func TestRichObject(t *testing.T) {
	var p PhotoSize
	if !richObject(parseRichValue(t, `{"file_id":"AgAC","file_unique_id":"AQAD","width":320,"height":240,"file_size":1234,"other":[1]}`), &p) {
		t.Fatal("richObject = false for a PhotoSize object")
	}
	if want := (PhotoSize{FileID: "AgAC", FileUniqueID: "AQAD", Width: 320, Height: 240, FileSize: 1234}); p != want {
		t.Fatalf("photo size %+v, want %+v", p, want)
	}
	var u User
	if !richObject(parseRichValue(t, `{"id":99,"is_bot":true,"first_name":"Tether"}`), &u) || u != (User{ID: 99, IsBot: true, FirstName: "Tether"}) {
		t.Fatalf("user %+v", u)
	}
	for _, raw := range []string{`5`, `"x"`, `[]`, `null`, `[{"file_id":"AgAC"}]`, `{"file_id":5}`} {
		var q PhotoSize
		if richObject(parseRichValue(t, raw), &q) {
			t.Errorf("richObject(%s) = true, want false", raw)
		}
	}
}

func TestRichWrongValueInUpdateBatch(t *testing.T) {
	const raw = `[
		{"update_id":1,"message":{"message_id":10,"chat":{"id":5,"type":"private"},"rich_message":5}},
		{"update_id":2,"message":{"message_id":11,"from":{"id":7,"first_name":"Ana"},"chat":{"id":5,"type":"private"},
			"date":1700000000,"text":"hello @tether_test_bot","entities":[{"type":"mention","offset":6,"length":16}]}},
		{"update_id":3,"edited_message":{"message_id":12,"chat":{"id":5,"type":"private"},"rich_message":{"blocks":"x"}}}]`
	var ups []Update
	if err := json.Unmarshal([]byte(raw), &ups); err != nil {
		t.Fatalf("the batch must decode: %v", err)
	}
	if len(ups) != 3 || ups[0].UpdateID != 1 || ups[1].UpdateID != 2 || ups[2].UpdateID != 3 {
		t.Fatalf("updates: %+v", ups)
	}
	want := Message{
		MessageID: 11,
		From:      &User{ID: 7, FirstName: "Ana"},
		Chat:      Chat{ID: 5, Type: "private"},
		Date:      1700000000,
		Text:      "hello @tether_test_bot",
		Entities:  []MessageEntity{{Type: "mention", Offset: 6, Length: 16}},
	}
	if ups[1].Message == nil || !reflect.DeepEqual(*ups[1].Message, want) {
		t.Fatalf("plain message %+v, want %+v", ups[1].Message, want)
	}
	if ups[0].Message == nil || string(ups[0].Message.RichMessage) != "5" {
		t.Fatalf("wrong rich_message not kept as raw JSON: %+v", ups[0].Message)
	}
	for _, m := range []*Message{ups[0].Message, ups[2].EditedMessage} {
		if d := richOf(*m); d == nil || len(d.blocks) != 0 || d.Failed() {
			t.Fatalf("message %d: document %+v, want a document with no blocks", m.MessageID, d)
		}
	}
}

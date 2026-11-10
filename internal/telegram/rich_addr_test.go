package telegram

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// addrPara returns the blocks JSON of one paragraph with the RichText value
// textJSON.
func addrPara(textJSON string) string {
	return `[{"type":"paragraph","text":` + textJSON + `}]`
}

func TestRichAddressing(t *testing.T) {
	const photo = `"photo":[{"file_id":"AgAC","file_unique_id":"AQAD","width":320,"height":240}]`
	name32 := strings.Repeat("a", 29) + "bot"
	botUser := &User{ID: 99, IsBot: true, FirstName: "Tether"}
	mention := func(offset int) richEntity {
		return richEntity{Type: "mention", Offset: offset, Length: 11, Text: "@tether_bot"}
	}
	cases := []struct {
		name, blocks, text string
		ents               []richEntity
	}{
		{"plain string", addrPara(`"hello @tether_bot"`), "hello @tether_bot", []richEntity{mention(6)}},
		{"mention span", addrPara(`["hi ",{"type":"mention","text":"@tether_bot","username":"tether_bot"}]`),
			"hi @tether_bot", []richEntity{mention(3)}},
		{"text_mention span", addrPara(`["ask ",{"type":"text_mention","text":"Tether","user":{"id":99,"is_bot":true,"first_name":"Tether"}}," now"]`),
			"ask Tether now", []richEntity{{Type: "text_mention", Offset: 4, Length: 6, Text: "Tether", User: botUser}}},
		{"text_mention span with no user", addrPara(`["ask ",{"type":"text_mention","text":"Tether","user":5}]`), "ask Tether", nil},
		{"command with the bot name", addrPara(`"/status@tether_bot"`), "/status@tether_bot",
			[]richEntity{{Type: "bot_command", Offset: 0, Length: 18, Text: "/status@tether_bot"}}},
		{"bot_command span", addrPara(`[{"type":"bot_command","text":"/status","bot_command":"status"}," now"]`), "/status now",
			[]richEntity{{Type: "bot_command", Offset: 0, Length: 7, Text: "/status"}}},
		{"command after a word", addrPara(`"run /new now"`), "run /new now",
			[]richEntity{{Type: "bot_command", Offset: 4, Length: 4, Text: "/new"}}},
		{"command on line 2", `[{"type":"paragraph","text":"first"},{"type":"paragraph","text":"/new"}]`, "first\n/new",
			[]richEntity{{Type: "bot_command", Offset: 6, Length: 4, Text: "/new"}}},
		{"code span", addrPara(`["run ",{"type":"code","text":"@tether_bot"}]`), "run " + richMask, nil},
		{"pre block", `[{"type":"pre","text":"@tether_bot /new"}]`, richMask, nil},
		{"link label", addrPara(`{"type":"url","text":"@tether_bot","url":"https://example.com/"}`), richMask, nil},
		{"mention span in a link label", addrPara(`{"type":"url","text":{"type":"mention","text":"@tether_bot"},"url":"https://example.com/"}`), richMask, nil},
		{"hidden URL", addrPara(`["ad ",{"type":"url","text":"here","url":"https://example.com/?u=@tether_bot"}]`), "ad " + richMask, nil},
		{"e-mail address", addrPara(`"mail a@tether_bot now"`), "mail a@tether_bot now", nil},
		{"after a backtick and after a slash", addrPara("\"`@tether_bot t.me/@tether_bot\""), "`@tether_bot t.me/@tether_bot", nil},
		{"path", addrPara(`"see /usr/bin and a/b"`), "see /usr/bin and a/b", nil},
		{"command with a short bot name", addrPara(`"/new@x and /new@tether_bot@x"`), "/new@x and /new@tether_bot@x", nil},
		{"command after code", addrPara(`[{"type":"code","text":"x"},"/new"]`), richMask + "/new", nil},
		{"emoji before the mention", addrPara(`"👍 @tether_bot"`), "👍 @tether_bot", []richEntity{mention(3)}},
		{"emoji before a mention span", addrPara(`["👍👍",{"type":"mention","text":"@tether_bot"}]`), "👍👍@tether_bot", []richEntity{mention(4)}},
		{"user name of 32 characters", addrPara(`"hi @` + name32 + `!"`), "hi @" + name32 + "!",
			[]richEntity{{Type: "mention", Offset: 3, Length: 33, Text: "@" + name32}}},
		{"user name of 33 characters", addrPara(`"hi @a` + name32 + `"`), "hi @a" + name32, nil},
		{"user name of 1 character", addrPara(`"hi @a"`), "hi @a", nil},
		{"typed U+FFFC before the mention", addrPara(`"\uFFFC@tether_bot hello"`), richMask + "@tether_bot hello", []richEntity{mention(1)}},
		{"leading spaces before a command", addrPara(`"   /new@tether_bot"`), "/new@tether_bot",
			[]richEntity{{Type: "bot_command", Offset: 0, Length: 15, Text: "/new@tether_bot"}}},
		{"empty paragraph before a command", `[{"type":"paragraph","text":""},{"type":"paragraph","text":" /new"}]`, "/new",
			[]richEntity{{Type: "bot_command", Offset: 0, Length: 4, Text: "/new"}}},
		{"leading spaces before a mention span", addrPara(`["  ",{"type":"mention","text":" @tether_bot "}," "]`), "@tether_bot",
			[]richEntity{mention(0)}},
		{"photo caption", `[{"type":"photo",` + photo + `,"caption":{"text":"look @tether_bot","credit":"by @other_bot"}}]`,
			"look @tether_bot\nby @other_bot",
			[]richEntity{mention(5), {Type: "mention", Offset: 20, Length: 10, Text: "@other_bot"}}},
		{"table cell and quotation credit", `[{"type":"table","cells":[[{"text":"a"},{"text":"@tether_bot"}]]},{"type":"blockquote","blocks":[],"credit":"@tether_bot"}]`,
			"a | @tether_bot\n@tether_bot", []richEntity{mention(4), mention(16)}},
		{"no blocks", `[]`, "", nil},
	}
	for _, c := range cases {
		d := richDocOf(t, c.blocks)
		text, ents := d.Addressing()
		if text != c.text {
			t.Errorf("%s: text %q, want %q", c.name, text, c.text)
		}
		if len(ents) != len(c.ents) || (len(ents) > 0 && !reflect.DeepEqual(ents, c.ents)) {
			t.Errorf("%s: entities %+v, want %+v", c.name, ents, c.ents)
		}
		if d.Failed() {
			t.Errorf("%s: the document failed", c.name)
		}
	}
}

// TestRichAddressingMatchesEntityText checks each entity against the UTF-16
// rule of a classic MessageEntity.
func TestRichAddressingMatchesEntityText(t *testing.T) {
	d := richDocOf(t, `[{"type":"paragraph","text":["é👍 ",{"type":"bold","text":"@one_bot"}," 👍 /start@two_bot 𝒳",{"type":"text_mention","text":"👍Ana","user":{"id":7}}]},
		{"type":"paragraph","text":"@three_bot"}]`)
	text, ents := d.Addressing()
	if len(ents) != 4 {
		t.Fatalf("entities %+v, want 4", ents)
	}
	prev := 0
	for _, e := range ents {
		got, ok := entityText(text, MessageEntity{Type: e.Type, Offset: e.Offset, Length: e.Length})
		if !ok || got != e.Text {
			t.Errorf("%+v: the classic rule gives %q", e, got)
		}
		if e.Offset < prev {
			t.Errorf("%+v: not sorted by Offset", e)
		}
		prev = e.Offset
	}
	if ents[0].Text != "@one_bot" || ents[1].Text != "/start@two_bot" || ents[2].Text != "👍Ana" || ents[3].Text != "@three_bot" {
		t.Errorf("entity texts: %+v", ents)
	}
}

func TestRichAddressingIsLinear(t *testing.T) {
	// 32768 characters, the limit of Telegram for one rich message.
	d := richDocOf(t, addrPara(`"`+strings.Repeat("@ab ", 8192)+`"`))
	best := time.Hour
	var ents []richEntity
	for range 3 {
		start := time.Now()
		_, ents = d.Addressing()
		best = min(best, time.Since(start))
	}
	if len(ents) != 8192 {
		t.Fatalf("%d entities, want 8192", len(ents))
	}
	if last := ents[8191]; last != (richEntity{Type: "mention", Offset: 32764, Length: 3, Text: "@ab"}) {
		t.Fatalf("last entity %+v", last)
	}
	if best > 100*time.Millisecond {
		t.Fatalf("Addressing took %v, want less than 100 ms", best)
	}
}

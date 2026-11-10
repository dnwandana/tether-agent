package telegram

import (
	"strings"
	"testing"
)

func TestRichMentionRemoved(t *testing.T) {
	cases := []struct{ name, blocks, want string }{
		{"bold mention", `[{"type":"paragraph","text":[{"type":"bold","text":"@tether_bot"}," hello"]}]`, "hello"},
		{"mention span and a separator", `[{"type":"paragraph","text":[{"type":"mention","text":"@tether_bot","username":"tether_bot"},", hello"]}]`, "hello"},
		{"other case and a colon", `[{"type":"paragraph","text":"@Tether_Bot: do this"}]`, "do this"},
		{"mention in the middle", `[{"type":"paragraph","text":"ask @tether_bot: now"}]`, "ask now"},
		{"mention in bold text", `[{"type":"paragraph","text":{"type":"bold","text":"@tether_bot do this"}}]`, "**do this**"},
		{"spaces end the match", `[{"type":"paragraph","text":[{"type":"bold","text":"@tether_bot "},"- item"]}]`, "- item"},
		{"code after the mention", `[{"type":"paragraph","text":[{"type":"mention","text":"@tether_bot"},{"type":"code","text":"x"},", y"]}]`, "`x`, y"},
		{"caption", `[{"type":"photo","photo":[{"file_id":"p1"}],"caption":{"text":"@tether_bot look"}}]`, "[photo]\nlook"},
		{"mention in its own paragraph", `[{"type":"paragraph","text":"@tether_bot"},{"type":"paragraph","text":"- do this"}]`, "- do this"},
	}
	for _, c := range cases {
		d := richDocOf(t, c.blocks)
		if got := d.Text("tether_bot"); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		// A "@" before the name has no effect.
		if got := d.Text("@tether_bot"); got != c.want {
			t.Errorf("%s, with @: %q, want %q", c.name, got, c.want)
		}
		// Without the bot user name, the text keeps each mention.
		if got := d.Text(""); !strings.Contains(strings.ToLower(got), "@tether_bot") {
			t.Errorf("%s: the text with no bot user name lost the mention: %q", c.name, got)
		}
	}
}

func TestRichMentionCaptionLine(t *testing.T) {
	d := richDocOf(t, `[{"type":"photo","photo":[{"file_id":"p1"}],"caption":{"text":"@tether_bot look"}}]`)
	lines := d.Lines("tether_bot")
	if len(lines) != 1 || lines[0].Media == nil || lines[0].Media.Caption != "look" {
		t.Fatalf("lines: %+v", lines)
	}
	if got := d.Lines("")[0].Media.Caption; got != "@tether_bot look" {
		t.Errorf("caption with no bot user name: %q", got)
	}
}

func TestRichMentionKept(t *testing.T) {
	cases := []struct{ name, blocks, has string }{
		{"code span", `[{"type":"paragraph","text":["run ",{"type":"code","text":"@tether_bot"}]}]`, "run `@tether_bot`"},
		{"pre block", `[{"type":"pre","text":"@tether_bot start"}]`, "@tether_bot start"},
		{"url of a url span", `[{"type":"paragraph","text":["see ",{"type":"url","text":"the page","url":"https://example.com/@tether_bot"}]}]`, "see [the page](https://example.com/@tether_bot)"},
		{"url span with the url as text", `[{"type":"paragraph","text":["see ",{"type":"url","text":"https://example.com/@tether_bot","url":"https://example.com/@tether_bot"}]}]`, "see https://example.com/@tether_bot"},
		{"only the mention", `[{"type":"paragraph","text":"@tether_bot"}]`, "@tether_bot"},
		{"only a bold mention", `[{"type":"paragraph","text":{"type":"bold","text":"@tether_bot"}}]`, "**@tether_bot**"},
		{"only a mention in a caption", `[{"type":"photo","photo":[{"file_id":"p1"}],"caption":{"text":"@tether_bot"}}]`, "[photo]\n@tether_bot"},
		{"longer user name", `[{"type":"paragraph","text":"@tether_bot2 hi"}]`, "@tether_bot2 hi"},
	}
	for _, c := range cases {
		d := richDocOf(t, c.blocks)
		got := d.Text("tether_bot")
		if got != d.Text("") {
			t.Errorf("%s: %q, want the text with no change: %q", c.name, got, d.Text(""))
		}
		if !strings.Contains(got, c.has) {
			t.Errorf("%s: %q does not contain %q", c.name, got, c.has)
		}
	}
	// A message with only the mention gives the lines of Lines("").
	d := richDocOf(t, `[{"type":"paragraph","text":"@tether_bot"}]`)
	if lines := d.Lines("tether_bot"); len(lines) != 1 || lines[0].Text != "@tether_bot" {
		t.Errorf("only the mention: lines %+v", lines)
	}
}

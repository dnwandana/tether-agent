package gateway

import (
	"regexp"
	"strings"
	"testing"
)

func TestFormatContext(t *testing.T) {
	for in, want := range map[int]string{
		200000:  "200K tokens",
		1000000: "1.0M tokens",
		1048576: "1.0M tokens",
		512:     "512 tokens",
		1500:    "1K tokens",
	} {
		if got := formatContext(in); got != want {
			t.Errorf("formatContext(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTipsAreCopiedFromSpec(t *testing.T) {
	if len(Tips) != 41 {
		t.Fatal(len(Tips))
	}
	for i, want := range map[int]string{
		0:  "/fork copies this session, so you can try a different idea and keep the original.",
		8:  "/archive hides a session from the list. /sessions archived shows the hidden sessions again.",
		17: "Send /usage all to see the tokens, the cache use, and the cost of all chats since the start.",
		19: "Run `tether update` in a terminal to install the newest release.",
		20: "/goal gives the agent a task that can need many turns. The agent works on it until the goal is done.",
	} {
		if Tips[i].Text != want {
			t.Fatal(i, Tips[i].Text)
		}
	}
	for _, tip := range Tips {
		if strings.Contains(tip.Text, "/resume brings it back") {
			t.Fatal(tip.Text)
		}
		if tip.Channel != "" && tip.Channel != "telegram" && tip.Channel != "cli" {
			t.Fatal(tip.Channel, tip.Text)
		}
	}
	for _, channel := range []string{"telegram", "cli"} {
		for range 50 {
			if tip := pickTip(channel); tip == "" {
				t.Fatal("empty tip")
			}
		}
	}
}

// hyphenCommand matches a slash command with "-" in its name. Telegram stops the command
// link at the "-".
var hyphenCommand = regexp.MustCompile(`(^|\s)/[a-z_]+-`)

func TestTipsForChannel(t *testing.T) {
	has := func(tips []string, prefix string) bool {
		for _, tip := range tips {
			if strings.HasPrefix(tip, prefix) {
				return true
			}
		}
		return false
	}
	tg, cli := tipsFor("telegram"), tipsFor("cli")
	for _, prefix := range []string{"/restart ", "/sethome ", "/reload_mcp ", "Send a sticker.", "Send a photo.", "Send a voice note."} {
		if !has(tg, prefix) {
			t.Errorf("telegram has no tip %q", prefix)
		}
		if has(cli, prefix) {
			t.Errorf("cli has the tip %q", prefix)
		}
	}
	if !has(cli, "/reload-mcp ") || has(tg, "/reload-mcp ") {
		t.Error("only the cli must have the /reload-mcp tip")
	}
	for _, tip := range tg {
		if hyphenCommand.MatchString(tip) {
			t.Errorf("telegram tip has a command with \"-\": %q", tip)
		}
	}
	if len(tg)+len(cli) != len(Tips)+countChannel("") {
		t.Fatal(len(tg), len(cli))
	}
}

// countChannel returns the number of tips with the channel tag channel.
func countChannel(channel string) int {
	n := 0
	for _, tip := range Tips {
		if tip.Channel == channel {
			n++
		}
	}
	return n
}

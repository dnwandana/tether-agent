package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// mediaTool is a fake tool with the name name. It returns out.
type mediaTool struct{ name, out string }

func (m mediaTool) Name() string     { return m.name }
func (mediaTool) Toolset() string    { return tools.SetMedia }
func (mediaTool) ParallelSafe() bool { return false }
func (m mediaTool) Schema() provider.ToolSchema {
	return provider.ToolSchema{Name: m.name, Description: m.name, Parameters: json.RawMessage(`{"type":"object"}`)}
}
func (m mediaTool) Run(context.Context, json.RawMessage, *tools.Env) (string, error) {
	return m.out, nil
}

// callMsg is an assistant message with one tool call.
func callMsg(id, name string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{tc(id, name, `{}`)}}
}

// resultMsg is the tool result of the call id.
func resultMsg(id, content string) provider.Message {
	return provider.Message{Role: provider.RoleTool, ToolCallID: id, Content: content}
}

// Hermes test_gateway_auto_append_ignores_media_examples_in_skill_docs.
func TestTurnMediaIgnoresExamplesOfOtherTools(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "How should I format gateway media?"},
		callMsg("call_skill", "skill_view"),
		resultMsg("call_skill", "Recommended pattern:\n```text\nMEDIA:/absolute/path/to/image.png\n```\n"),
		{Role: provider.RoleAssistant, Content: "Use a standalone media message."},
	}
	if got := turnMedia(msgs); len(got) != 0 {
		t.Fatalf("turnMedia = %q, want none", got)
	}
}

// Hermes test_gateway_auto_append_keeps_real_tts_media_tag.
func TestTurnMediaKeepsTTSTag(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "Say this as audio"},
		callMsg("call_tts", "text_to_speech"),
		resultMsg("call_tts", `{"success": true, "media_tag": "[[audio_as_voice]]\nMEDIA:/tmp/voice.ogg"}`),
		{Role: provider.RoleAssistant, Content: "Done."},
	}
	want := []string{"[[audio_as_voice]]", "MEDIA:/tmp/voice.ogg"}
	if got := turnMedia(msgs); !reflect.DeepEqual(got, want) {
		t.Fatalf("turnMedia = %q, want %q", got, want)
	}
}

func TestTurnMediaOrderAndDuplicates(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			tc("1", "text_to_speech", `{}`), tc("2", "text_to_speech", `{}`), tc("3", "text_to_speech_tool", `{}`),
		}},
		resultMsg("1", "MEDIA:/audio/same.ogg"),
		resultMsg("2", "MEDIA:/audio/same.ogg"),
		resultMsg("3", "MEDIA:/audio/different.mp3"),
	}
	want := []string{"MEDIA:/audio/same.ogg", "MEDIA:/audio/different.mp3"}
	if got := turnMedia(msgs); !reflect.DeepEqual(got, want) {
		t.Fatalf("turnMedia = %q, want %q", got, want)
	}
}

func TestTurnMediaNeedsPathAndExtension(t *testing.T) {
	msgs := []provider.Message{
		callMsg("1", "text_to_speech"),
		resultMsg("1", "MEDIA:/tmp/noext MEDIA:relative/a.mp3 MEDIA:~/home.MP3 MEDIA:C:\\v\\x.wav MEDIA:/tmp/a.exe"),
	}
	want := []string{"MEDIA:~/home.MP3", "MEDIA:C:\\v\\x.wav"}
	if got := turnMedia(msgs); !reflect.DeepEqual(got, want) {
		t.Fatalf("turnMedia = %q, want %q", got, want)
	}
}

func TestTurnMediaUnknownCallIgnored(t *testing.T) {
	msgs := []provider.Message{resultMsg("x", "MEDIA:/tmp/voice.ogg [[audio_as_voice]]")}
	if got := turnMedia(msgs); len(got) != 0 {
		t.Fatalf("turnMedia = %q, want none", got)
	}
}

func TestAppendMediaTags(t *testing.T) {
	media := []string{"[[audio_as_voice]]", "MEDIA:/tmp/v.mp3"}
	cases := []struct{ final, want string }{
		{"Done.", "Done.\n[[audio_as_voice]]\nMEDIA:/tmp/v.mp3"},
		// The model wrote a tag, so no tag is added (Hermes).
		{"Here: MEDIA:/tmp/v.mp3", "Here: MEDIA:/tmp/v.mp3"},
		{"See MEDIA:/tmp/other.png", "See MEDIA:/tmp/other.png"},
		// An empty answer gets no tags (Hermes returns before the append).
		{"", ""},
	}
	for _, c := range cases {
		if got := AppendMediaTags(c.final, media); got != c.want {
			t.Errorf("AppendMediaTags(%q) = %q, want %q", c.final, got, c.want)
		}
	}
	if got := AppendMediaTags("Done.", nil); got != "Done." {
		t.Errorf("AppendMediaTags with no media = %q", got)
	}
}

func TestRunTurnCollectsTTSMedia(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "text_to_speech", `{}`)), text("Done."))
	a, sess := newAgent(t, fp, mediaTool{name: "text_to_speech", out: "MEDIA:/data/media/tts-1.mp3 [[audio_as_voice]]"})
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "say hi", Toolsets: all()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Final != "Done." {
		t.Fatalf("Final = %q, want the model text only", res.Final)
	}
	want := []string{"[[audio_as_voice]]", "MEDIA:/data/media/tts-1.mp3"}
	if !reflect.DeepEqual(res.Media, want) {
		t.Fatalf("Media = %q, want %q", res.Media, want)
	}

	// The next turn has no tool call, so the file of the earlier turn is not sent again.
	fp.script = []step{text("It is 3 PM.")}
	res, err = a.RunTurn(ctx, Turn{Session: sess, UserText: "time?", Toolsets: all()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Media) != 0 {
		t.Fatalf("Media of the next turn = %q, want none", res.Media)
	}
}

func TestRunTurnIgnoresMediaOfOtherTools(t *testing.T) {
	fp := scripted(toolCalls(tc("c1", "browser_take_screenshot", `{}`)), text("Done."))
	a, sess := newAgent(t, fp, mediaTool{name: "browser_take_screenshot", out: "MEDIA:/data/cache/screenshots/s.png\nUse view_image to inspect it."})
	res, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "look", Toolsets: all()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Media) != 0 {
		t.Fatalf("Media = %q, want none", res.Media)
	}
}

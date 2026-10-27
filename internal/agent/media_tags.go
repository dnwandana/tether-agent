package agent

import (
	"regexp"
	"strings"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// autoAppendMediaTools are the tools whose MEDIA tags go to the chat when the answer has
// no tag (Hermes _AUTO_APPEND_MEDIA_TOOL_NAMES). Other tool results can hold MEDIA
// examples, for example in docs or logs, so they never count.
var autoAppendMediaTools = map[string]bool{"text_to_speech": true, "text_to_speech_tool": true}

// toolMediaRE matches a MEDIA tag in a tool result (Hermes _TOOL_MEDIA_RE). The path
// must be absolute, start with "~/", or start with a drive letter, and it must end with
// a deliverable extension.
var toolMediaRE = regexp.MustCompile(`(?i)MEDIA:((?:[A-Za-z]:[/\\]|/|~/)\S+\.(?:png|jpe?g|gif|webp|` +
	`mp4|mov|avi|mkv|webm|ogg|opus|mp3|wav|m4a|` +
	`flac|epub|pdf|zip|rar|7z|docx?|xlsx?|pptx?|` +
	`txt|csv|apk|ipa))`)

// voiceDirective asks the channel to send the audio as a voice note.
const voiceDirective = "[[audio_as_voice]]"

// turnMedia returns the MEDIA tags of the media tools in msgs, the messages of one turn
// (Hermes _collect_auto_append_media_tags). The tags keep their order and each tag is
// there once. When a result has the voice directive, the directive comes first.
func turnMedia(msgs []provider.Message) []string {
	names := map[string]string{}
	for _, m := range msgs {
		if m.Role != provider.RoleAssistant {
			continue
		}
		for _, c := range m.ToolCalls {
			if c.ID != "" && c.Name != "" {
				names[c.ID] = c.Name
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	voice := false
	for _, m := range msgs {
		if m.Role != provider.RoleTool || !autoAppendMediaTools[names[m.ToolCallID]] {
			continue
		}
		if !strings.Contains(m.Content, "MEDIA:") {
			continue
		}
		for _, sm := range toolMediaRE.FindAllStringSubmatch(m.Content, -1) {
			p := strings.TrimRight(strings.TrimSpace(sm[1]), `",}`)
			if tag := "MEDIA:" + p; p != "" && !seen[tag] {
				seen[tag] = true
				out = append(out, tag)
			}
		}
		if strings.Contains(m.Content, voiceDirective) {
			voice = true
		}
	}
	if voice && len(out) > 0 {
		out = append([]string{voiceDirective}, out...)
	}
	return out
}

// AppendMediaTags returns final with the media lines of TurnResult.Media at the end. A
// messaging channel calls it before it takes the files out of the answer, so a file of a
// media tool reaches the chat when the model left out its tag. The channel media policy
// still checks each file. An empty final, or a final that has a MEDIA tag already, does
// not change (Hermes gateway/run.py).
func AppendMediaTags(final string, media []string) string {
	if final == "" || len(media) == 0 || strings.Contains(final, "MEDIA:") {
		return final
	}
	return final + "\n" + strings.Join(media, "\n")
}

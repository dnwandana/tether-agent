package agent

import (
	_ "embed"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/skills"
)

//go:embed base_prompt.md
var basePrompt string

// skillDescMax is the maximum number of characters of a skill description in the index.
const skillDescMax = 1024

// promptTimeFormat is the time format of the CONTEXT section.
const promptTimeFormat = "2006-01-02 15:04 -07"

// PromptInput holds the parts of a system prompt.
type PromptInput struct {
	Base, Memory, Profile string
	SkillsIndex           []SkillLine
	ToolNotes             []string
	Now                   time.Time
	// TZ is the name of the time zone, for example "Asia/Jakarta". It can be empty.
	TZ, WorkDir, Channel string
	// ChatInfo describes the chat, for example the chat type and the topic on Telegram.
	ChatInfo string
}

// SkillLine is one line of the skills index.
type SkillLine struct {
	Name, Description string
}

// SkillLines converts the skills index of the loader to the lines of the SKILLS section.
func SkillLines(index []skills.IndexLine) []SkillLine {
	out := make([]SkillLine, len(index))
	for i, l := range index {
		out[i] = SkillLine{Name: l.Name, Description: l.Description}
	}
	return out
}

// BasePrompt returns the embedded base prompt.
func BasePrompt() string { return basePrompt }

// BuildSystemPrompt returns the system prompt. The parts are in this order: base, MEMORY,
// USER PROFILE, SKILLS, TOOL NOTES, and CONTEXT. A part with no content has no header. The
// output depends only on in, because the prompt cache of the provider needs the same bytes
// for the same input.
func BuildSystemPrompt(in PromptInput) string {
	var parts []string
	if s := strings.TrimSpace(in.Base); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(in.Memory); s != "" {
		parts = append(parts, "## MEMORY\n"+s)
	}
	if s := strings.TrimSpace(in.Profile); s != "" {
		parts = append(parts, "## USER PROFILE\n"+s)
	}
	if len(in.SkillsIndex) > 0 {
		var b strings.Builder
		b.WriteString("## SKILLS")
		for _, sk := range in.SkillsIndex {
			b.WriteString("\n- ")
			b.WriteString(oneLine(sk.Name))
			b.WriteString(": ")
			b.WriteString(capRunes(oneLine(sk.Description), skillDescMax))
		}
		parts = append(parts, b.String())
	}
	if len(in.ToolNotes) > 0 {
		var b strings.Builder
		b.WriteString("## TOOL NOTES")
		for _, n := range in.ToolNotes {
			b.WriteString("\n- ")
			b.WriteString(n)
		}
		parts = append(parts, b.String())
	}
	parts = append(parts, contextSection(in))
	return strings.Join(parts, "\n\n")
}

// contextSection returns the CONTEXT section: the date, the time zone, the working
// directory, the channel, and the chat.
func contextSection(in PromptInput) string {
	var b strings.Builder
	b.WriteString("## CONTEXT\n- Date and time: ")
	b.WriteString(in.Now.Format(promptTimeFormat))
	if in.TZ != "" {
		b.WriteString("\n- Time zone: ")
		b.WriteString(in.TZ)
	}
	if in.WorkDir != "" {
		b.WriteString("\n- Working directory: ")
		b.WriteString(in.WorkDir)
	}
	if in.Channel != "" {
		b.WriteString("\n- Channel: ")
		b.WriteString(in.Channel)
	}
	if in.ChatInfo != "" {
		b.WriteString("\n- Chat: ")
		b.WriteString(in.ChatInfo)
	}
	return b.String()
}

// The tool notes of DefaultToolNotes.
const (
	noteCurl    = "To call an API, use curl through the terminal tool: curl -sS -w '\\n%{http_code}' ...; put JSON bodies in a file and use --data @file."
	notePDF     = "For a PDF, download it with curl and convert it with pdftotext."
	noteBrowser = "If web_search or web_fetch fails, use the browser tools."
)

// DefaultToolNotes returns the tool notes for the visible tools, in a fixed order.
func DefaultToolNotes(visible map[string]bool) []string {
	var out []string
	if visible["terminal"] {
		out = append(out, noteCurl)
	}
	if visible["web_fetch"] {
		out = append(out, notePDF)
	}
	if visible["browser_navigate"] {
		out = append(out, noteBrowser)
	}
	return out
}

// oneLine joins the lines and the runs of white space of s into single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// capRunes cuts s to n runes.
func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

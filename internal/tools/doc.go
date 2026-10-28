package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// docIntro is the text at the top of docs/TOOLS.md.
const docIntro = `# Tools

<!-- tether gen-tools-doc writes this file. Do not edit it. Run:
     go run ./cmd/tether gen-tools-doc > docs/TOOLS.md -->

This file lists the built-in tools of Tether Agent by toolset. Each entry shows
the description and the JSON schema of the parameters that the model sees.

- ` + "`toolsets.<channel>`" + ` in config.yaml selects the toolsets of each channel. A tool
  outside these toolsets is hidden from the model.
- The MCP tools are not in this file. Each MCP server adds its tools at run time in the
  toolset ` + "`mcp_<server>`" + `.
- Some tools need a host program or a config value. The entry of such a tool has a
  **Needs** line.
`

// toolNeeds returns what a tool needs on the host or in the config, or "".
func toolNeeds(name string) string {
	switch {
	case strings.HasPrefix(name, "browser_"):
		return "Chrome or Chromium (browser.chrome_path, CHROME_PATH, or PATH). Without it, the tool returns an error."
	case name == "execute_code":
		return "python3 on PATH. Without it, the tool is not registered."
	case name == "view_image":
		return "a vision provider: agent.vision_provider, or vision: true on the primary provider. Without it, the tool is not registered."
	case name == "text_to_speech":
		return "media.tts. The default command needs edge-tts. With an empty backend, the tool is not registered."
	case name == "transcribe_audio":
		return "media.stt. The default command needs whisper-cli. With an empty backend, the tool is not registered."
	case name == "generate_image":
		return "media.image. It has no default backend, so the tool is not registered until you set one."
	case name == "delegate_task":
		return "the gateway role for background: true. The chat role runs the tasks in the foreground."
	}
	return ""
}

// GenerateMarkdown returns docs/TOOLS.md for the tools of r. It groups the tools by
// toolset in name order. Each tool has a heading, its description, and its parameters
// schema as indented JSON.
func GenerateMarkdown(r *Registry) string {
	bySet := map[string][]Tool{}
	for _, t := range r.All() {
		bySet[t.Toolset()] = append(bySet[t.Toolset()], t)
	}
	sets := make([]string, 0, len(bySet))
	for s := range bySet {
		sets = append(sets, s)
	}
	sort.Strings(sets)

	var b strings.Builder
	b.WriteString(docIntro)
	b.WriteString("\n| Toolset | Tools |\n| --- | --- |\n")
	for _, s := range sets {
		names := make([]string, 0, len(bySet[s]))
		for _, t := range bySet[s] {
			names = append(names, "`"+t.Name()+"`")
		}
		fmt.Fprintf(&b, "| `%s` | %s |\n", s, strings.Join(names, ", "))
	}
	for _, s := range sets {
		fmt.Fprintf(&b, "\n## %s\n", s)
		for _, t := range bySet[s] {
			sc := t.Schema()
			fmt.Fprintf(&b, "\n### %s\n\n", t.Name())
			if d := strings.TrimSpace(sc.Description); d != "" {
				b.WriteString(d)
				b.WriteString("\n\n")
			}
			if needs := toolNeeds(t.Name()); needs != "" {
				fmt.Fprintf(&b, "**Needs:** %s\n\n", needs)
			}
			b.WriteString("```json\n")
			b.WriteString(schemaJSON(sc.Parameters))
			b.WriteString("\n```\n")
		}
	}
	return b.String()
}

// schemaJSON returns the schema as JSON indented by 2 spaces. A schema that is not valid
// JSON stays as it is.
func schemaJSON(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "{}"
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return strings.TrimSpace(out.String())
}

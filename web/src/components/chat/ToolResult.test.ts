import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import ToolResult from "./ToolResult.vue"

const tool = (name: string, text: string, over = {}) => ({
  call: "c1",
  name,
  emoji: "",
  preview: "",
  args: "{}",
  state: "done",
  duration_ms: 100,
  text,
  cut: false,
  ...over,
})

describe("ToolResult", () => {
  it("shows a terminal result with its exit code", () => {
    const w = mount(ToolResult, {
      props: {
        tool: tool("terminal", "FAIL\tcron\n[exit code: 1]", {
          args: '{"command":"go test ./..."}',
        }),
      },
    })
    expect(w.find(".result-h .path").text()).toBe("go test ./...")
    expect(w.find(".result-h .c-bad").text()).toBe("exit 1")
    expect(w.find(".term").text()).toContain("$ go test ./...")
    expect(w.find(".term").text()).toContain("FAIL\tcron")
    expect(w.find(".term").text()).not.toContain("[exit code: 1]")
  })

  it("shows a background start", () => {
    const w = mount(ToolResult, {
      props: {
        tool: tool("terminal", '{"pid":4242,"log_path":"/tmp/bg-4242.log"}', {
          args: '{"command":"npm run dev","background":true}',
        }),
      },
    })
    expect(w.text()).toContain("background · PID 4242")
    expect(w.text()).toContain("/tmp/bg-4242.log")
  })

  it("shows a patch as a diff", () => {
    const w = mount(ToolResult, {
      props: {
        tool: tool(
          "patch",
          "patched a.go (1 replacement)\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n x\n-old\n+new\n",
        ),
      },
    })
    expect(w.find(".diff .hunk").text()).toBe("@@ -1,2 +1,2 @@")
    expect(w.findAll(".diff .dl.add")).toHaveLength(1)
    expect(w.findAll(".diff .dl.del")).toHaveLength(1)
    expect(w.find(".result-h").text()).toContain("+1")
    expect(w.find(".result-h").text()).toContain("−1")
  })

  it("shows fetched text as closed, untrusted Markdown", () => {
    const text =
      '<untrusted_tool_result source="web_fetch">\nWarning.\n\n## Principles\n<script>alert(1)</script>\n</untrusted_tool_result>'
    const w = mount(ToolResult, {
      props: {
        tool: tool("web_fetch", text, { args: '{"url":"https://keepachangelog.com/en/1.1.0/"}' }),
      },
    })
    const d = w.find("details.result.untrusted")
    expect(d.exists()).toBe(true)
    expect(d.attributes("open")).toBeUndefined()
    expect(d.find("summary").text()).toContain("⚠ Untrusted content")
    expect(d.find("summary").text()).toContain("https://keepachangelog.com/en/1.1.0/")
    expect(d.find(".md").html()).toContain("<h2>Principles</h2>")
    expect(d.find(".md").html()).not.toContain("<script")
  })

  it("shows one line, the navigate view, and the plain fallback", () => {
    expect(
      mount(ToolResult, { props: { tool: tool("write_file", "wrote 120 bytes to /tmp/a.md") } })
        .find(".result-h")
        .text(),
    ).toContain("wrote 120 bytes to /tmp/a.md")
    const nav = mount(ToolResult, {
      props: {
        tool: tool(
          "browser_navigate",
          "title: Releases\nfinal_url: https://github.com/o/r/releases\n\nv0.23.0",
        ),
      },
    })
    expect(nav.text()).toContain("Releases")
    expect(nav.text()).toContain("https://github.com/o/r/releases")
    expect(nav.text()).toContain("v0.23.0")
    expect(
      mount(ToolResult, { props: { tool: tool("memory", "Saved. <b>x</b>") } })
        .find("pre.code-block")
        .text(),
    ).toBe("Saved. <b>x</b>")
  })

  it("shows an image from a file link, and nothing for todo_list", () => {
    const w = mount(ToolResult, {
      props: {
        tool: tool(
          "browser_take_screenshot",
          "MEDIA:/api/files/ab12\nUse view_image to inspect it.",
        ),
      },
    })
    expect(w.find("img").attributes("src")).toBe("/api/files/ab12")
    expect(mount(ToolResult, { props: { tool: tool("todo_list", "[x] a") } }).html()).toBe(
      "<!--v-if-->",
    )
  })

  it("offers Show all for a cut text", async () => {
    const t = tool("memory", "long", { cut: true })
    const w = mount(ToolResult, { props: { tool: t } })
    expect(w.text()).toContain("The text is cut at 16 KB.")
    await w.find('[data-act="show-all"]').trigger("click")
    expect(w.emitted("show-all")).toEqual([[t]])
  })
})

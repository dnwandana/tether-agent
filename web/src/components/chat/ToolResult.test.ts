import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
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
const blocks = (c: Element) => Array.from(c.querySelectorAll('[data-slot="code-block"]'))
const bodyText = (b: Element) =>
  b.querySelector('[data-slot="code-block-content"]')!.textContent!.trim()

describe("ToolResult", () => {
  it("shows a terminal result with its exit code", () => {
    const r = render(ToolResult, {
      props: {
        tool: tool("terminal", "FAIL\tcron\n[exit code: 1]", {
          args: '{"command":"go test ./..."}',
        }),
      },
    })
    const [b] = blocks(r.container)
    expect(b.getAttribute("data-variant")).toBe("destructive")
    expect(b.querySelector('[data-slot="code-block-header"]')!.textContent).toContain(
      "$ go test ./...",
    )
    expect(bodyText(b)).toContain("FAIL\tcron")
    expect(bodyText(b)).not.toContain("[exit code: 1]")
    expect(b.querySelector('[data-slot="code-block-footer"]')!.textContent!.trim()).toBe("exit 1")
  })

  it("shows the code and the python run of execute_code", () => {
    const r = render(ToolResult, {
      props: {
        tool: tool("execute_code", "42\n[exit code: 0]", { args: '{"code":"print(6*7)"}' }),
      },
    })
    const [code, term] = blocks(r.container)
    expect(bodyText(code)).toBe("print(6*7)")
    expect(term.querySelector('[data-slot="code-block-header"]')!.textContent).toContain("$ python")
  })

  it("shows a background start", () => {
    const r = render(ToolResult, {
      props: {
        tool: tool("terminal", '{"pid":4242,"log_path":"/tmp/bg-4242.log"}', {
          args: '{"command":"npm run dev","background":true}',
        }),
      },
    })
    expect(r.container.textContent).toContain("background · PID 4242")
    expect(r.container.textContent).toContain("/tmp/bg-4242.log")
  })

  it("shows a patch as a diff", () => {
    const r = render(ToolResult, {
      props: {
        tool: tool(
          "patch",
          "patched a.go (1 replacement)\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n x\n-old\n+new\n",
        ),
      },
    })
    const lines = Array.from(r.container.querySelectorAll('[data-slot="code-block-line"]'))
    expect(lines.filter((l) => l.getAttribute("data-variant") === "added")).toHaveLength(1)
    expect(lines.filter((l) => l.getAttribute("data-variant") === "removed")).toHaveLength(1)
    const head = r.container.querySelector('[data-slot="code-block-header"]')!
    expect(head.textContent).toContain("+1")
    expect(head.textContent).toContain("−1")
  })

  it("shows fetched text as closed, untrusted Markdown", async () => {
    const text =
      '<untrusted_tool_result source="web_fetch">\nWarning.\n\n## Principles\n<script>alert(1)</script>\n</untrusted_tool_result>'
    const r = render(ToolResult, {
      props: {
        tool: tool("web_fetch", text, { args: '{"url":"https://keepachangelog.com/en/1.1.0/"}' }),
      },
    })
    const trigger = screen.getByRole("button", { name: /Untrusted content/ })
    expect(trigger.querySelector("svg.lucide-triangle-alert")).not.toBeNull()
    expect(trigger.textContent).not.toContain("⚠")
    expect(trigger.textContent).toContain("https://keepachangelog.com/en/1.1.0/")
    expect(trigger.getAttribute("aria-expanded")).toBe("false")
    expect(trigger.closest("[data-untrusted]")).not.toBeNull()
    expect(r.container.querySelector(".typeset")).toBeNull()
    await userEvent.setup().click(trigger)
    const md = r.container.querySelector(".typeset.typeset-chat")!
    expect(md.innerHTML).toContain("<h2>Principles</h2>")
    expect(md.innerHTML).not.toContain("<script")
  })

  it("shows one line, the navigate view, and the plain fallback", () => {
    const one = render(ToolResult, {
      props: { tool: tool("write_file", "wrote 120 bytes to /tmp/a.md") },
    })
    expect(bodyText(blocks(one.container)[0])).toContain("wrote 120 bytes to /tmp/a.md")
    one.unmount()
    const nav = render(ToolResult, {
      props: {
        tool: tool(
          "browser_navigate",
          "title: Releases\nfinal_url: https://github.com/o/r/releases\n\nv0.23.0",
        ),
      },
    })
    expect(nav.container.textContent).toContain("Releases")
    expect(nav.container.textContent).toContain("https://github.com/o/r/releases")
    expect(nav.container.textContent).toContain("v0.23.0")
    nav.unmount()
    const plain = render(ToolResult, { props: { tool: tool("memory", "Saved. <b>x</b>") } })
    expect(bodyText(blocks(plain.container)[0])).toBe("Saved. <b>x</b>")
  })

  it("shows an image from a file link, and nothing for todo_list", () => {
    const r = render(ToolResult, {
      props: {
        tool: tool(
          "browser_take_screenshot",
          "MEDIA:/api/files/ab12\nUse view_image to inspect it.",
        ),
      },
    })
    expect(r.container.querySelector("img")!.getAttribute("src")).toBe("/api/files/ab12")
    r.unmount()
    const todo = render(ToolResult, { props: { tool: tool("todo_list", "[x] a") } })
    expect(todo.container.innerHTML).toBe("<!--v-if-->")
  })

  it("offers Show all for a cut text", async () => {
    const t = tool("memory", "long", { cut: true })
    const r = render(ToolResult, { props: { tool: t } })
    expect(r.container.textContent).toContain("The text is cut at 16 KB.")
    await userEvent.setup().click(screen.getByRole("button", { name: "Show all" }))
    expect(r.emitted()["show-all"]).toEqual([[t]])
  })
})

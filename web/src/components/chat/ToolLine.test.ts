import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import ToolLine from "./ToolLine.vue"

const tool = (over = {}) => ({
  call: "c1",
  name: "terminal",
  emoji: "💻",
  preview: "go test ./...",
  args: '{"command":"go test ./..."}',
  state: "done",
  duration_ms: 3900,
  text: "ok  \tpkg\t0.188s",
  cut: false,
  ...over,
})
const line = (b: HTMLElement) => b.querySelector('[data-slot="trace-line"]')!
const chev = (b: HTMLElement) => {
  const icons = b.querySelectorAll('[data-slot="trace-icon"]')
  return icons[icons.length - 1]
}

const icon = (b: HTMLElement) => b.querySelector('[data-slot="trace-icon"]')!
const desc = (b: HTMLElement) => b.querySelector('[data-slot="trace-description"]')!

describe("ToolLine", () => {
  it("shows a finished line with the icon of the tool name", () => {
    render(ToolLine, { props: { tool: tool({ emoji: "📖" }) } })
    const b = screen.getByRole("button")
    const t = b.textContent!
    expect(icon(b).querySelector("svg.lucide-square-terminal")).not.toBeNull()
    expect(t).not.toContain("📖")
    expect(t).toContain("terminal")
    expect(t).toContain('"go test ./..."')
    expect(desc(b).textContent!.replace(/\s+/g, " ").trim()).toBe("3.9 s · done")
    expect(desc(b).querySelector("svg.lucide-check")!.classList.contains("text-success")).toBe(true)
    expect(t).not.toMatch(/[✓✗▸▾]/u)
    expect(line(b).getAttribute("data-status")).toBeNull()
    expect(b.getAttribute("aria-expanded")).toBe("false")
    expect(chev(b).querySelector("svg.lucide-chevron-right")).not.toBeNull()
  })

  it("shows the exit code of a failed command", () => {
    render(ToolLine, {
      props: {
        tool: tool({ state: "error", duration_ms: 4200, text: "FAIL\n[exit code: 1]" }),
        selected: true,
      },
    })
    const b = screen.getByRole("button")
    expect(desc(b).textContent!.replace(/\s+/g, " ").trim()).toBe("4.2 s · exit 1")
    expect(desc(b).querySelector("svg.lucide-x")!.classList.contains("text-destructive")).toBe(true)
    expect(line(b).getAttribute("data-status")).toBe("error")
    expect(b.getAttribute("data-selected")).toBe("true")
    expect(b.getAttribute("aria-expanded")).toBe("true")
    expect(chev(b).querySelector("svg.lucide-chevron-down")).not.toBeNull()
  })

  it("shows a running line, a plain error, and the icon of each tool", () => {
    const run = render(ToolLine, { props: { tool: tool({ state: "running", text: "" }) } })
    const rb = run.container.querySelector("button")!
    expect(line(rb).getAttribute("data-status")).toBe("running")
    expect(desc(rb).textContent!.trim()).toBe("running")
    expect(desc(rb).querySelector("svg")).toBeNull()
    run.unmount()
    const err = render(ToolLine, {
      props: { tool: tool({ name: "web_fetch", emoji: "📄", state: "error", text: "HTTP 404" }) },
    })
    const eb = err.container.querySelector("button")!
    expect(desc(eb).textContent).toContain("error")
    expect(desc(eb).querySelector("svg.lucide-x")).not.toBeNull()
    expect(icon(eb).querySelector("svg.lucide-file-down")).not.toBeNull()
    err.unmount()
    const read = render(ToolLine, { props: { tool: tool({ name: "read_file", emoji: "🛠" }) } })
    expect(
      icon(read.container.querySelector("button")!).querySelector("svg.lucide-file-text"),
    ).not.toBeNull()
    read.unmount()
    render(ToolLine, { props: { tool: tool({ name: "mystery_tool", emoji: "" }) } })
    expect(icon(screen.getByRole("button")).querySelector("svg.lucide-wrench")).not.toBeNull()
  })

  it("shows the preview of a child tool", () => {
    render(ToolLine, {
      props: { tool: tool({ preview: "[1/2] terminal", state: "running", text: "" }) },
    })
    expect(screen.getByRole("button").textContent).toContain('"[1/2] terminal"')
  })

  it("emits select and hides todo_list", async () => {
    const r = render(ToolLine, { props: { tool: tool() } })
    await userEvent.setup().click(screen.getByRole("button"))
    expect(r.emitted().select).toHaveLength(1)
    r.unmount()
    const todo = render(ToolLine, { props: { tool: tool({ name: "todo_list", emoji: "📋" }) } })
    expect(todo.container.querySelector("button")).toBeNull()
  })
})

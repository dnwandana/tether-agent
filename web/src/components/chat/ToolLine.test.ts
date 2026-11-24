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

describe("ToolLine", () => {
  it("shows a finished line", () => {
    render(ToolLine, { props: { tool: tool() } })
    const b = screen.getByRole("button")
    const t = b.textContent!
    expect(t).toContain("💻")
    expect(t).toContain("terminal")
    expect(t).toContain('"go test ./..."')
    expect(b.querySelector('[data-slot="trace-description"]')!.textContent).toContain("3.9 s")
    expect(t).toContain("✓ done")
    expect(line(b).getAttribute("data-status")).toBeNull()
    expect(b.getAttribute("aria-expanded")).toBe("false")
    expect(chev(b).textContent!.trim()).toBe("▸")
  })

  it("shows the exit code of a failed command", () => {
    render(ToolLine, {
      props: {
        tool: tool({ state: "error", duration_ms: 4200, text: "FAIL\n[exit code: 1]" }),
        selected: true,
      },
    })
    const b = screen.getByRole("button")
    expect(b.textContent).toContain("✗ exit 1")
    expect(b.textContent).toContain("4.2 s")
    expect(line(b).getAttribute("data-status")).toBe("error")
    expect(b.getAttribute("data-selected")).toBe("true")
    expect(b.getAttribute("aria-expanded")).toBe("true")
    expect(chev(b).textContent!.trim()).toBe("▾")
  })

  it("shows a running line and a plain error", () => {
    const run = render(ToolLine, { props: { tool: tool({ state: "running", text: "" }) } })
    const rb = run.container.querySelector("button")!
    expect(line(rb).getAttribute("data-status")).toBe("running")
    expect(rb.textContent).toContain("running")
    run.unmount()
    render(ToolLine, {
      props: { tool: tool({ name: "web_fetch", emoji: "📄", state: "error", text: "HTTP 404" }) },
    })
    expect(screen.getByRole("button").textContent).toContain("✗ error")
  })

  it("shows the preview of a child tool", () => {
    render(ToolLine, {
      props: { tool: tool({ preview: "🔀 [1/2] terminal", state: "running", text: "" }) },
    })
    expect(screen.getByRole("button").textContent).toContain('"🔀 [1/2] terminal"')
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

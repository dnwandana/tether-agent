import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
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

describe("ToolLine", () => {
  it("shows a finished line", () => {
    const w = mount(ToolLine, { props: { tool: tool() } })
    const t = w.text()
    expect(t).toContain("💻")
    expect(t).toContain("terminal")
    expect(t).toContain('"go test ./..."')
    expect(t).toContain("3.9 s")
    expect(t).toContain("✓ done")
    expect(w.find(".toolline").classes()).toContain("is-done")
    expect(w.find(".chev").text()).toBe("▸")
  })

  it("shows the exit code of a failed command", () => {
    const w = mount(ToolLine, {
      props: {
        tool: tool({ state: "error", duration_ms: 4200, text: "FAIL\n[exit code: 1]" }),
        selected: true,
      },
    })
    expect(w.text()).toContain("✗ exit 1")
    expect(w.text()).toContain("4.2 s")
    expect(w.find(".toolline").classes()).toEqual(
      expect.arrayContaining(["is-error", "is-selected"]),
    )
    expect(w.find(".chev").text()).toBe("▾")
  })

  it("shows a running line and a plain error", () => {
    expect(
      mount(ToolLine, { props: { tool: tool({ state: "running", text: "" }) } })
        .find(".spin")
        .exists(),
    ).toBe(true)
    expect(
      mount(ToolLine, {
        props: { tool: tool({ name: "web_fetch", emoji: "📄", state: "error", text: "HTTP 404" }) },
      }).text(),
    ).toContain("✗ error")
  })

  it("emits select and hides todo_list", async () => {
    const w = mount(ToolLine, { props: { tool: tool() } })
    await w.find(".toolline").trigger("click")
    expect(w.emitted("select")).toHaveLength(1)
    expect(
      mount(ToolLine, { props: { tool: tool({ name: "todo_list", emoji: "📋" }) } })
        .find(".toolline")
        .exists(),
    ).toBe(false)
  })
})

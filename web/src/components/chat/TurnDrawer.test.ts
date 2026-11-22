import { describe, it, expect, vi, beforeEach } from "vitest"
import { flushPromises, mount } from "@vue/test-utils"
import type { ToolView } from "../../api/types.gen"

vi.mock("../../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../../stores/ui", async (orig) => ({ ...(await orig<object>()), toast: vi.fn() }))
import { api } from "../../api/client"
import { toast } from "../../stores/ui"
import { session } from "../../stores/session"
import TurnDrawer from "./TurnDrawer.vue"

const tool = {
  call: "call_1",
  name: "terminal",
  emoji: "💻",
  preview: "go test ./...",
  args: '{"command":"go test ./..."}',
  state: "done",
  duration_ms: 1200,
  text: "ok\n[exit code: 0]",
  cut: false,
} as ToolView

function phone(on: boolean) {
  window.matchMedia = ((q: string) => ({
    matches: on,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
}
const mk = (t: ToolView | null = tool) =>
  mount(TurnDrawer, {
    props: { open: true, tool: t },
    global: { stubs: { ToolResult: true } },
    attachTo: document.body,
  })

beforeEach(() => phone(false))

describe("TurnDrawer", () => {
  it("shows the tool call", () => {
    const w = mk()
    expect(w.find(".aside-h").text()).toContain("Turn details")
    expect(w.text()).toContain("call_1")
    expect(w.text()).toContain("1.2 s")
    expect(w.find(".code-block").text()).toBe('{\n  "command": "go test ./..."\n}')
    expect(w.find(".chip").text()).toBe("done")
    w.unmount()
  })

  it("shows the hint with no tool", () => {
    const w = mk(null)
    expect(w.text()).toContain("Select a tool line to see its full arguments and its result.")
    w.unmount()
  })

  it("closes on the close button, the scrim, and Esc", async () => {
    const w = mk()
    await w.find('button[aria-label="Close the details"]').trigger("click")
    await w.find("#aside-scrim").trigger("click")
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }))
    expect(w.emitted("close")).toHaveLength(3)
    w.unmount()
  })

  it("ignores Esc when it is closed", async () => {
    const w = mount(TurnDrawer, {
      props: { open: false, tool },
      global: { stubs: { ToolResult: true } },
    })
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }))
    expect(w.emitted("close")).toBeUndefined()
    w.unmount()
  })

  it("ignores Esc that another layer took first", async () => {
    const w = mk()
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", cancelable: true }))
    const e = new KeyboardEvent("keydown", { key: "Escape", cancelable: true })
    e.preventDefault()
    window.dispatchEvent(e)
    expect(w.emitted("close")).toHaveLength(1)
    w.unmount()
  })

  it("closes on a swipe down of more than 70 px on a phone", async () => {
    phone(true)
    const w = mk()
    const h = w.find(".aside-h")
    const touch = (type: string, y: number) =>
      h.trigger(type, { touches: [{ clientY: y }], changedTouches: [{ clientY: y }] })
    await touch("touchstart", 100)
    await touch("touchmove", 130)
    await touch("touchend", 130)
    expect(w.emitted("close")).toBeUndefined()
    await touch("touchstart", 100)
    await touch("touchmove", 200)
    await touch("touchend", 200)
    expect(w.emitted("close")).toHaveLength(1)
    w.unmount()
  })
  it("fetches the full text on show-all", async () => {
    session.id = "s/1"
    vi.mocked(api).mockResolvedValueOnce({ ...tool, call: "call/1", text: "FULL TEXT", cut: false })
    const w = mk({ ...tool, call: "call/1", text: "cut", cut: true })
    const tr = () => w.findComponent({ name: "ToolResult" })
    await tr().vm.$emit("show-all", { ...tool, call: "call/1", cut: true })
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s%2F1/tools/call%2F1")
    expect(tr().props("tool")).toMatchObject({ text: "FULL TEXT", cut: false })
    w.unmount()
  })

  it("toasts an error when show-all fails", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("Not found."))
    vi.mocked(toast).mockClear()
    const w = mk({ ...tool, cut: true })
    await w.findComponent({ name: "ToolResult" }).vm.$emit("show-all", tool)
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("Not found.", "error")
    w.unmount()
  })
})

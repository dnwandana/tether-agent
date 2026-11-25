import { it, expect, vi, beforeEach, afterEach } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
import { defineComponent, h } from "vue"
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
import { status } from "../../stores/status"
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

function width(px: number) {
  window.matchMedia = ((q: string) => {
    const min = /min-width:\s*(\d+)px/.exec(q)
    const max = /max-width:\s*(\d+)px/.exec(q)
    const matches = (!min || px >= Number(min[1])) && (!max || px <= Number(max[1]))
    return { matches, media: q, addEventListener() {}, removeEventListener() {} }
  }) as any
}
// The stub shows the text of the tool and sends show-all on a click.
const ToolResult = defineComponent({
  props: ["tool"],
  emits: ["show-all"],
  setup:
    (p, { emit }) =>
    () =>
      h(
        "button",
        { type: "button", onClick: () => emit("show-all", p.tool) },
        "show all " + p.tool.text,
      ),
})
const mk = (t: ToolView | null = tool, open = true) =>
  render(TurnDrawer, { props: { open, tool: t }, global: { stubs: { ToolResult } } })
// The scrim of PanelFrame.
const scrim = () => document.querySelector('[data-slot="panel-scrim"]') as HTMLElement

beforeEach(() => width(800))
afterEach(() => cleanup())

it("closes when a side panel opens, so only one side column shows", async () => {
  const { emitted, rerender } = mk()
  document.dispatchEvent(new CustomEvent("tether:panel-open"))
  expect(emitted().close).toHaveLength(1)
  await rerender({ open: false, tool })
  document.dispatchEvent(new CustomEvent("tether:panel-open"))
  expect(emitted().close).toHaveLength(1)
})

it("shows the tool call", () => {
  mk()
  expect(screen.getByText("Turn details")).toBeTruthy()
  expect(screen.getByText("call_1")).toBeTruthy()
  expect(screen.getByText("1.2 s")).toBeTruthy()
  expect(document.querySelector("pre")!.textContent).toBe('{\n  "command": "go test ./..."\n}')
  expect(screen.getByText("done")).toBeTruthy()
})

it("shows the icon of the tool name and the mode from MODES", () => {
  status.mode = "guarded"
  mk({ ...tool, emoji: "📖" })
  const name = screen.getByText("terminal")
  expect(name.parentElement!.querySelector("svg.lucide-square-terminal")).not.toBeNull()
  expect(document.body.textContent).not.toContain("📖")
  const mode = screen.getByText("Mode").nextElementSibling!
  expect(mode.textContent!.trim()).toBe("Guarded")
  expect(mode.querySelector("svg.lucide-shield")).not.toBeNull()
  status.mode = "odd"
})

it("shows the hint with no tool", () => {
  mk(null)
  expect(
    screen.getByText("Select a tool line to see its full arguments and its result."),
  ).toBeTruthy()
})

it("closes on the close button, the scrim, and Esc", async () => {
  const user = userEvent.setup()
  const { emitted } = mk()
  await user.click(screen.getByRole("button", { name: /^Close/ }))
  await fireEvent.click(scrim())
  await user.keyboard("{Escape}")
  expect(emitted().close).toHaveLength(3)
})

it("ignores Esc when it is closed", async () => {
  const user = userEvent.setup()
  const { emitted } = mk(tool, false)
  await user.keyboard("{Escape}")
  expect(emitted().close).toBeUndefined()
})

it("ignores Esc that another layer took first", async () => {
  const user = userEvent.setup()
  const { emitted } = mk()
  const layer = document.createElement("div")
  layer.setAttribute("data-dismissable-layer", "")
  document.body.appendChild(layer)
  await user.keyboard("{Escape}")
  expect(emitted().close).toBeUndefined()
  layer.remove()
  await user.keyboard("{Escape}")
  expect(emitted().close).toHaveLength(1)
})

it("closes on a swipe down of more than 70 px on a phone", async () => {
  width(390)
  const { emitted } = mk()
  const title = screen.getByText("Turn details")
  const touch = (fn: typeof fireEvent.touchStart, y: number) =>
    fn(title, { touches: [{ clientY: y }], changedTouches: [{ clientY: y }] })
  await touch(fireEvent.touchStart, 100)
  await touch(fireEvent.touchMove, 130)
  await touch(fireEvent.touchEnd, 130)
  expect(emitted().close).toBeUndefined()
  await touch(fireEvent.touchStart, 100)
  await touch(fireEvent.touchMove, 200)
  await touch(fireEvent.touchEnd, 200)
  expect(emitted().close).toHaveLength(1)
})

it("fetches the full text on show-all", async () => {
  session.id = "s/1"
  vi.mocked(api).mockResolvedValueOnce({ ...tool, call: "call/1", text: "FULL TEXT", cut: false })
  const user = userEvent.setup()
  mk({ ...tool, call: "call/1", text: "cut", cut: true })
  await user.click(screen.getByRole("button", { name: "show all cut" }))
  await flushPromises()
  expect(api).toHaveBeenCalledWith("GET", "/api/sessions/s%2F1/tools/call%2F1")
  expect(screen.getByRole("button", { name: "show all FULL TEXT" })).toBeTruthy()
})

it("toasts an error when show-all fails", async () => {
  vi.mocked(api).mockRejectedValueOnce(new Error("Not found."))
  vi.mocked(toast).mockClear()
  const user = userEvent.setup()
  mk({ ...tool, cut: true })
  await user.click(screen.getByRole("button", { name: /^show all/ }))
  await flushPromises()
  expect(toast).toHaveBeenCalledWith("Not found.", "error")
})

const mode = () => document.querySelector("[data-mode]")!.getAttribute("data-mode")

it("docks at 1024 px and wider, is modal below, and is a sheet below 600 px", () => {
  width(1280)
  const a = mk()
  expect(mode()).toBe("docked")
  a.unmount()
  width(800)
  const b = mk()
  expect(mode()).toBe("modal")
  b.unmount()
  width(390)
  mk()
  expect(mode()).toBe("sheet")
})

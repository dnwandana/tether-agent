import { flushPromises, mount } from "@vue/test-utils"
import { reactive } from "vue"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const route = reactive({ query: {} as Record<string, string> })
const replace = vi.fn()
const secure = { value: true }
vi.mock("vue-router", () => ({ useRoute: () => route, useRouter: () => ({ replace }) }))
vi.mock("../../api/client", () => ({
  api: vi.fn(async () => ({
    commands: [
      { name: "/usage", description: "Tokens and cost" },
      { name: "/stop", description: "Stop the task" },
    ],
    skills: [{ name: "/release-notes", description: "Write release notes" }],
  })),
  postForm: vi.fn(async () => ({ ok: true })),
  runCommand: vi.fn(async () => ({ request_id: "r1" })),
}))
vi.mock("../../stores/ui", () => ({ isSecure: () => secure.value, toast: vi.fn() }))

import Composer from "./Composer.vue"
import { turn } from "../../stores/turn"
import { postForm, runCommand } from "../../api/client"
import { toast } from "../../stores/ui"

async function mountIt() {
  const w = mount(Composer, { attachTo: document.body })
  await flushPromises()
  return { w, ta: w.find("textarea"), el: w.find("textarea").element as HTMLTextAreaElement }
}
const lastForm = () => vi.mocked(postForm).mock.calls.at(-1)![1] as FormData

beforeEach(() => {
  vi.mocked(postForm).mockClear()
  vi.mocked(runCommand).mockClear()
  vi.mocked(toast).mockClear()
  replace.mockClear()
  route.query = {}
  secure.value = true
  turn.view = null
  turn.acks = []
  turn.queue = []
})
afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ""
})

describe("Composer", () => {
  it("grows from one line to eight lines", async () => {
    const { ta, el } = await mountIt()
    Object.defineProperty(el, "scrollHeight", { configurable: true, get: () => 400 })
    await ta.setValue("1\n2\n3\n4\n5\n6\n7\n8\n9\n10")
    expect(el.style.height).toBe("194px")
    Object.defineProperty(el, "scrollHeight", { configurable: true, get: () => 44 })
    await ta.setValue("1")
    expect(el.style.height).toBe("44px")
  })

  it("sends a message with Enter, and not with Shift+Enter", async () => {
    const { w, ta, el } = await mountIt()
    await ta.setValue("hello")
    await ta.trigger("keydown", { key: "Enter", shiftKey: true })
    expect(postForm).not.toHaveBeenCalled()
    await ta.trigger("keydown", { key: "Enter" })
    await flushPromises()
    expect(vi.mocked(postForm).mock.calls[0][0]).toBe("/api/messages")
    expect(lastForm().get("text")).toBe("hello")
    expect(lastForm().get("client_id")).toMatch(/^[0-9a-f]{32}$/)
    // The server reads client_id only as the first part.
    expect([...lastForm().keys()][0]).toBe("client_id")
    expect(el.value).toBe("")
    expect(w.emitted("sent")).toEqual([[{ text: "hello", files: [] }]])
  })

  it("keeps the draft and the client id after an error", async () => {
    vi.mocked(postForm).mockRejectedValueOnce(new Error("The gateway is stopping."))
    const { w, ta, el } = await mountIt()
    await ta.setValue("hello")
    await ta.trigger("keydown", { key: "Enter" })
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("The gateway is stopping.", "error")
    expect(el.value).toBe("hello")
    expect(w.emitted("failed")).toEqual([[{ text: "hello", files: [] }]])
    const first = lastForm().get("client_id")
    await ta.trigger("keydown", { key: "Enter" })
    await flushPromises()
    expect(lastForm().get("client_id")).toBe(first)
  })

  it("runs a built-in command and sends a skill as a message", async () => {
    const { w, ta } = await mountIt()
    await ta.setValue("/usage today")
    await ta.trigger("keydown", { key: "Enter" })
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/usage today")
    expect(w.emitted("reply")).toEqual([[{ request_id: "r1" }]])
    await ta.setValue("/release-notes v0.24")
    await ta.trigger("keydown", { key: "Enter" })
    await flushPromises()
    expect(lastForm().get("text")).toBe("/release-notes v0.24")
  })

  it("completes a command from the slash menu", async () => {
    const { w, ta, el } = await mountIt()
    await ta.setValue("/us")
    expect(w.findAll(".slash-pop .menu-item .mono").map((x) => x.text())).toEqual(["/usage"])
    await ta.trigger("keydown", { key: "Enter" })
    expect(el.value).toBe("/usage ")
    expect(runCommand).not.toHaveBeenCalled()
    expect(w.find(".slash-pop").exists()).toBe(false)
  })

  it("shows Stop while a turn runs and the field is empty", async () => {
    turn.view = { running: true, text: "", tools: [], plan: [], queue: [] } as never
    const { w, ta } = await mountIt()
    expect(w.find('[aria-label="Send"]').exists()).toBe(false)
    await w.find('[aria-label="Stop the task"]').trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/stop")
    await ta.setValue("also check the docs")
    expect(w.find('[aria-label="Send"]').exists()).toBe(true)
  })

  it("turns Send off with nothing to send", async () => {
    const { w } = await mountIt()
    expect(w.find('[aria-label="Send"]').attributes("disabled")).toBeDefined()
  })

  it("hides the voice button on plain HTTP", async () => {
    secure.value = false
    const a = await mountIt()
    expect(a.w.find('[aria-label="Hold to record"]').exists()).toBe(false)
    expect(a.w.text()).toContain("Needs HTTPS or localhost.")
    a.w.unmount()
    secure.value = true
    const b = await mountIt()
    expect(b.w.find('[aria-label="Hold to record"]').exists()).toBe(true)
    expect(b.w.text()).not.toContain("Needs HTTPS or localhost.")
  })

  it("attaches files and sends them", async () => {
    const { w, ta } = await mountIt()
    const input = w.find('input[type="file"][multiple]')
    Object.defineProperty(input.element, "files", {
      configurable: true,
      value: [new File(["# notes"], "notes.md", { type: "text/markdown" })],
    })
    await input.trigger("change")
    expect(w.find(".attach-row .att").text()).toContain("📎 notes.md")
    await ta.trigger("keydown", { key: "Enter" })
    await flushPromises()
    expect((lastForm().getAll("files") as File[]).map((f) => f.name)).toEqual(["notes.md"])
    expect(w.emitted("sent")).toEqual([[{ text: "", files: ["notes.md"] }]])
  })

  it("shows the live acks and the queue, and runs the queue now", async () => {
    turn.acks = [
      { id: "a1", kind: "steer", text: "↪️ Got it.", request_id: "", buttons: [] },
      { id: "a2", kind: "reply", text: "⏸ Paused.", request_id: "r1", buttons: [] },
    ] as never
    turn.queue = [{ kind: "skill", preview: "/release-notes" }]
    const { w } = await mountIt()
    expect(w.findAll(".live-acks .ack").map((a) => a.text())).toEqual(["↪️ Got it."])
    await w.find(".queue-strip button").trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/now")
  })

  it("sets --dock-h from the ResizeObserver", async () => {
    let cb: ResizeObserverCallback | undefined
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(f: ResizeObserverCallback) {
          cb = f
        }
        observe() {}
        disconnect() {}
      },
    )
    const { w } = await mountIt()
    cb!(
      [
        {
          borderBoxSize: [{ blockSize: 132, inlineSize: 600 }],
          contentRect: { height: 100 },
        } as unknown as ResizeObserverEntry,
      ],
      {} as ResizeObserver,
    )
    expect((w.element.parentElement as HTMLElement).style.getPropertyValue("--dock-h")).toBe(
      "132px",
    )
    expect(w.emitted("resize")).toEqual([[132]])
  })

  it("takes the draft of the palette", async () => {
    route.query = { draft: "/usage " }
    const { el } = await mountIt()
    expect(el.value).toBe("/usage ")
    expect(replace).toHaveBeenCalledWith({ query: {} })
  })
})

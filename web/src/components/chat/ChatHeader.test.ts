import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn() }))
import { runCommand } from "../../api/client"
import { toast, confirm } from "../../stores/ui"
import { session } from "../../stores/session"
import ChatHeader from "./ChatHeader.vue"

const Blank = { render: () => null }
const reply = (text: string, buttons: { text: string; data: string }[][] = []) => ({
  request_id: "r1",
  text,
  buttons,
  edit: false,
})

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "chat", component: Blank },
      { path: "/goals/:session", name: "goal", component: Blank },
    ],
  })
  await router.push("/")
  const w = mount(ChatHeader, {
    global: { plugins: [router], stubs: { SidePanel: true } },
    attachTo: document.body,
  })
  return { w, router }
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!

beforeEach(() => {
  vi.mocked(runCommand).mockReset().mockResolvedValue(reply("Done."))
  vi.mocked(toast).mockReset()
  vi.mocked(confirm).mockReset()
  Object.assign(session, {
    id: "s1",
    title: "Old",
    model: "glm-4.6",
    entry: "primary",
    effort: "",
    goalLine: "",
    context: null,
  })
})

const rectOf = (left: number, width: number) =>
  ({ left, width, right: left + width, top: 0, bottom: 32, height: 32, x: left, y: 0 }) as DOMRect
const effortReply = () =>
  vi.mocked(runCommand).mockResolvedValueOnce(
    reply("Pick.", [
      [
        { text: "a", data: "/effort a" },
        { text: "b", data: "/effort b" },
        { text: "c", data: "/effort c" },
      ],
    ]),
  )

async function openAt(boxLeft: number, boxWidth: number, chipLeft: number) {
  effortReply()
  const { w } = await mk()
  vi.spyOn(w.find(".chat-h").element, "getBoundingClientRect").mockReturnValue(
    rectOf(boxLeft, boxWidth),
  )
  const chip = w.findAll("button.chip")[1]
  vi.spyOn(chip.element, "getBoundingClientRect").mockReturnValue(rectOf(chipLeft, 120))
  await chip.trigger("click")
  await flushPromises()
  return w
}

afterEach(() => vi.restoreAllMocks())

describe("ChatHeader", () => {
  it("renames the session", async () => {
    const { w } = await mk()
    await w.find(".chat-title").trigger("click")
    const i = w.find("input.chat-title-input")
    await i.setValue("Release notes")
    await i.trigger("keydown", { key: "Enter" })
    expect(runCommand).toHaveBeenCalledWith("/rename Release notes")
    expect(runCommand).toHaveBeenCalledTimes(1)
    w.unmount()
  })

  it("shows the context meter and runs Compact", async () => {
    session.context = { tokens: 12345, window: 200000, threshold: 0.5 }
    const { w } = await mk()
    expect(w.find(".ctx").text()).toContain("~12.3k / 200k")
    await btn(w, "Compact").trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/compact")
    w.unmount()
  })

  it("opens the model picker from the /model reply", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(
      reply("Pick a provider.", [
        [{ text: "primary", data: "/model primary" }],
        [{ text: "b", data: "/model b" }],
        [{ text: "c", data: "/model c" }],
      ]),
    )
    const { w } = await mk()
    await w.findAll("button.chip")[0].trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/model")
    await btn(w, "primary").trigger("click")
    expect(runCommand).toHaveBeenLastCalledWith("/model primary")
    w.unmount()
  })

  it("asks before a delete and sends the confirm action", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(
      reply('Delete "Old"?', [
        [
          { text: "🗑 Delete", data: "/delete s1 confirm" },
          { text: "Cancel", data: "/sessions" },
        ],
      ]),
    )
    vi.mocked(confirm).mockResolvedValue(true)
    const { w } = await mk()
    await w.find('button[aria-label="Session menu"]').trigger("click")
    await btn(w, "Delete…").trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/delete s1")
    expect(confirm).toHaveBeenCalledWith({
      title: 'Delete "Old"?',
      body: "",
      confirmLabel: "🗑 Delete",
      danger: true,
      markdown: true,
    })
    expect(runCommand).toHaveBeenLastCalledWith("/delete s1 confirm")
    w.unmount()
  })

  it("opens the goal from the goal chip", async () => {
    session.goalLine = "↻ Goal 8/20: two tests fail"
    const { w, router } = await mk()
    await w.find(".chip-acc").trigger("click")
    await flushPromises()
    expect(w.find(".chip-acc").text()).toBe("↻ Goal 8/20: two tests fail")
    expect(router.currentRoute.value.fullPath).toBe("/goals/s1")
    w.unmount()
  })

  it("shows a plain reply as a toast and closes the popover on Esc", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(
      reply("Pick.", [
        [
          { text: "a", data: "/effort a" },
          { text: "·", data: "·" },
        ],
        [{ text: "c", data: "/effort c" }],
      ]),
    )
    const { w } = await mk()
    await w.findAll("button.chip")[1].trigger("click")
    await flushPromises()
    expect(btn(w, "·").attributes("disabled")).toBeDefined()
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", cancelable: true }))
    await flushPromises()
    expect(w.find(".popover").exists()).toBe(false)
    await w.findAll("button.chip")[0].trigger("click")
    await flushPromises()
    expect(vi.mocked(toast).mock.calls.at(-1)!.slice(0, 2)).toEqual(["Done.", "info"])
    w.unmount()
  })
  it("revokes the export URL after a delay, not at once", async () => {
    vi.useFakeTimers()
    try {
      const revoke = vi.fn()
      URL.createObjectURL = vi.fn(() => "blob:x")
      URL.revokeObjectURL = revoke
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => ({ ok: true, status: 200, blob: async () => new Blob(["x"]) })),
      )
      const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
      const { w } = await mk()
      await w.find('button[aria-label="Session menu"]').trigger("click")
      await btn(w, "Export JSONL").trigger("click")
      await vi.advanceTimersByTimeAsync(0)
      expect(click).toHaveBeenCalled()
      expect(revoke).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(30_000)
      expect(revoke).toHaveBeenCalledWith("blob:x")
      click.mockRestore()
      w.unmount()
    } finally {
      vi.useRealTimers()
      vi.unstubAllGlobals()
    }
  })

  it("shows New session for an empty title", async () => {
    session.title = ""
    const { w } = await mk()
    expect(w.find(".chat-title").text()).toBe("New session")
    expect(w.find(".chat-title").classes()).toContain("c-3")
    w.unmount()
  })

  it("shows the model chip as model (entry)", async () => {
    const { w } = await mk()
    expect(w.findAll("button.chip")[0].text()).toBe("model glm-4.6 (primary) ▾")
    session.entry = ""
    await flushPromises()
    expect(w.findAll("button.chip")[0].text()).toBe("model glm-4.6 ▾")
    w.unmount()
  })

  it("hides the session actions while no session exists", async () => {
    session.id = ""
    session.title = ""
    const { w } = await mk()
    expect(w.find("button.chat-title").exists()).toBe(false)
    expect(w.find(".chat-title").text()).toBe("New session")
    await w.find('button[aria-label="Session menu"]').trigger("click")
    const items = w.findAll(".menu .menu-item").map((b) => b.text())
    expect(items).toEqual(["Custom prompt…"])
    w.unmount()
  })

  it("links to the Pages help of the wiki", async () => {
    const { w } = await mk()
    const a = w.find("a.help-link")
    expect(a.attributes("href")).toBe(
      "https://github.com/dnwandana/tether-agent/wiki/Dashboard#pages",
    )
    expect(a.attributes("rel")).toBe("noreferrer")
    w.unmount()
  })
  it("opens the reply popover under the clicked chip", async () => {
    const w = await openAt(100, 1000, 340)
    expect(w.find(".popover").attributes("style")).toContain("left: 240px")
    w.unmount()
  })

  it("keeps the popover inside the header at the right end", async () => {
    const w = await openAt(100, 1000, 1050)
    expect(w.find(".popover").attributes("style")).toContain("left: 572px")
    w.unmount()
  })

  it("keeps the popover inside the header on a phone", async () => {
    const w = await openAt(0, 390, 200)
    expect(w.find(".popover").attributes("style")).toContain("left: 8px")
    w.unmount()
  })

  it("puts help, the menu, and Details in one end group", async () => {
    const { w } = await mk()
    const end = w.find(".chat-h-end")
    expect(end.find("a.help-link").exists()).toBe(true)
    expect(end.find('button[aria-label="Session menu"]').exists()).toBe(true)
    expect(end.find("button.aside-toggle").text()).toBe("Details")
    expect(w.find(".chat-h > .grow").exists()).toBe(false)
    w.unmount()
  })

  it("moves Compact into the session menu on a phone", async () => {
    session.context = { tokens: 12345, window: 200000, threshold: 0.5 }
    const { w } = await mk()
    expect(w.find(".ctx button").classes()).toContain("hide-phone")
    await w.find('button[aria-label="Session menu"]').trigger("click")
    const item = w.findAll(".menu .menu-item").find((b) => b.text() === "Compact")!
    expect(item.classes()).toContain("only-phone")
    await item.trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/compact")
    w.unmount()
  })

  it("has no Compact menu item without a context meter", async () => {
    const { w } = await mk()
    await w.find('button[aria-label="Session menu"]').trigger("click")
    expect(w.findAll(".menu .menu-item").map((b) => b.text())).not.toContain("Compact")
    w.unmount()
  })
})

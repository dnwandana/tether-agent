import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { flushPromises } from "@vue/test-utils"
import { render, screen, within, cleanup } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
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
  const view = render(ChatHeader, { global: { plugins: [router] } })
  return { ...view, router }
}

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

const modelChip = () => screen.getByRole("button", { name: /^model / })
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

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe("ChatHeader", () => {
  it("renames the session", async () => {
    const user = userEvent.setup()
    await mk()
    await user.click(screen.getByRole("button", { name: "Old" }))
    const i = screen.getByRole("textbox", { name: "Session title" })
    await user.clear(i)
    await user.type(i, "Release notes{Enter}")
    expect(runCommand).toHaveBeenCalledWith("/rename Release notes")
    expect(runCommand).toHaveBeenCalledTimes(1)
  })

  it("shows the context meter and runs Compact", async () => {
    session.context = { tokens: 12345, window: 200000, threshold: 0.5 }
    const user = userEvent.setup()
    await mk()
    expect(screen.queryByRole("button", { name: "Compact" })).toBeNull()
    await user.click(screen.getByRole("button", { name: /~12\.3k \/ 200k/ }))
    const bar = await screen.findByRole("progressbar", { name: "Context used" })
    expect(Number(bar.getAttribute("aria-valuenow"))).toBeCloseTo(6.17, 1)
    const mark = document.querySelector('[data-slot="context-threshold"]') as HTMLElement
    expect(mark.style.left).toBe("50%")
    await user.click(screen.getByRole("button", { name: "Compact" }))
    expect(runCommand).toHaveBeenCalledWith("/compact")
  })

  it("opens the model picker from the /model reply", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(
      reply("Pick a provider.", [
        [{ text: "primary", data: "/model primary" }],
        [{ text: "b", data: "/model b" }],
        [{ text: "c", data: "/model c" }],
      ]),
    )
    const user = userEvent.setup()
    await mk()
    await user.click(screen.getByRole("button", { name: /^model / }))
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/model")
    expect(screen.getByRole("menu").textContent).toContain("Pick a provider.")
    await user.click(screen.getByRole("menuitem", { name: "primary" }))
    expect(runCommand).toHaveBeenLastCalledWith("/model primary")
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
    const user = userEvent.setup()
    await mk()
    await user.click(screen.getByRole("button", { name: "Session menu" }))
    await user.click(screen.getByRole("menuitem", { name: "Delete…" }))
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
  })

  it("opens the goal from the goal chip", async () => {
    session.goalLine = "↻ Goal 8/20: two tests fail"
    const user = userEvent.setup()
    const { router } = await mk()
    const goal = screen.getByRole("button", { name: "Goal 8/20: two tests fail" })
    expect(goal.getAttribute("title")).toBe("Goal 8/20: two tests fail")
    expect(goal.querySelector("svg.lucide-refresh-cw")).not.toBeNull()
    expect(goal.textContent).not.toContain("↻")
    await user.click(goal)
    await flushPromises()
    expect(router.currentRoute.value.fullPath).toBe("/goals/s1")
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
    const user = userEvent.setup()
    await mk()
    await user.click(screen.getByRole("button", { name: /^effort / }))
    await flushPromises()
    expect(screen.getByRole("menuitem", { name: "·" }).getAttribute("aria-disabled")).toBe("true")
    await user.keyboard("{Escape}")
    await flushPromises()
    expect(screen.queryByRole("menu")).toBeNull()
    await user.click(screen.getByRole("button", { name: /^model / }))
    await flushPromises()
    expect(vi.mocked(toast).mock.calls.at(-1)!.slice(0, 2)).toEqual(["Done.", "info"])
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
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
      await mk()
      await user.click(screen.getByRole("button", { name: "Session menu" }))
      await user.click(screen.getByRole("menuitem", { name: "Export JSONL" }))
      await vi.advanceTimersByTimeAsync(0)
      expect(click).toHaveBeenCalled()
      expect(revoke).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(30_000)
      expect(revoke).toHaveBeenCalledWith("blob:x")
    } finally {
      vi.useRealTimers()
      vi.unstubAllGlobals()
    }
  })

  it("shows New session for an empty title", async () => {
    session.title = ""
    await mk()
    const t = screen.getByRole("button", { name: "New session" })
    expect(t.classList.contains("text-muted-foreground")).toBe(true)
  })

  it("shows the model chip as model (entry)", async () => {
    await mk()
    expect(modelChip().textContent!.trim()).toBe("model glm-4.6 (primary)")
    session.entry = ""
    await flushPromises()
    expect(modelChip().textContent!.trim()).toBe("model glm-4.6")
    expect(modelChip().querySelector("svg.lucide-chevron-down")).not.toBeNull()
  })

  it("hides the session actions while no session exists", async () => {
    session.id = ""
    session.title = ""
    const user = userEvent.setup()
    await mk()
    expect(screen.queryByRole("button", { name: "New session" })).toBeNull()
    expect(screen.getByText("New session")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Session menu" }))
    expect(screen.getAllByRole("menuitem").map((b) => b.textContent!.trim())).toEqual([
      "Custom prompt…",
    ])
  })

  it("links to the Pages help of the wiki", async () => {
    await mk()
    const a = screen.getByRole("link", { name: /Help for the pages/ })
    expect(a.getAttribute("href")).toBe(
      "https://github.com/dnwandana/tether-agent/wiki/Dashboard#pages",
    )
    expect(a.getAttribute("rel")).toBe("noreferrer")
  })

  it("opens the reply menu under the clicked chip", async () => {
    effortReply()
    const user = userEvent.setup()
    await mk()
    const chip = screen.getByRole("button", { name: /^effort / })
    await user.click(chip)
    await flushPromises()
    const menu = screen.getByRole("menu")
    expect(chip.getAttribute("aria-expanded")).toBe("true")
    expect(chip.getAttribute("aria-controls")).toBe(menu.id)
    expect(menu.getAttribute("data-side")).toBe("bottom")
    expect(menu.getAttribute("data-align")).toBe("start")
  })

  it("puts help, the menu, and Details in one end group", async () => {
    await mk()
    const link = screen.getByRole("link", { name: /Help for the pages/ })
    const menu = screen.getByRole("button", { name: "Session menu" })
    const details = screen.getByRole("button", { name: "Details" })
    expect(menu.parentElement).toBe(link.parentElement)
    expect(details.parentElement).toBe(link.parentElement)
    expect(details.classList.contains("max-tab:hidden")).toBe(true)
  })

  it("moves Compact into the session menu on a phone", async () => {
    session.context = { tokens: 12345, window: 200000, threshold: 0.5 }
    const user = userEvent.setup()
    await mk()
    await user.click(screen.getByRole("button", { name: "Session menu" }))
    const item = screen.getByRole("menuitem", { name: "Compact" })
    expect(item.classList.contains("tab:hidden")).toBe(true)
    await user.click(item)
    await flushPromises()
    expect(runCommand).toHaveBeenLastCalledWith("/compact")
  })

  it("has no Compact menu item without a context meter", async () => {
    const user = userEvent.setup()
    await mk()
    await user.click(screen.getByRole("button", { name: "Session menu" }))
    expect(screen.getAllByRole("menuitem").map((b) => b.textContent!.trim())).not.toContain(
      "Compact",
    )
  })

  it("saves and clears the custom prompt in a dialog", async () => {
    const user = userEvent.setup()
    await mk()
    await user.click(screen.getByRole("button", { name: "Session menu" }))
    await user.click(screen.getByRole("menuitem", { name: "Custom prompt…" }))
    const dlg = await screen.findByRole("dialog", { name: "Custom prompt of this session" })
    await user.type(within(dlg).getByRole("textbox", { name: "Custom prompt" }), "Be short.")
    await user.click(within(dlg).getByRole("button", { name: "Save" }))
    expect(runCommand).toHaveBeenLastCalledWith("/prompt set Be short.")
    await flushPromises()
    expect(screen.queryByRole("dialog")).toBeNull()
    await user.click(screen.getByRole("button", { name: "Session menu" }))
    await user.click(screen.getByRole("menuitem", { name: "Custom prompt…" }))
    await user.click(await screen.findByRole("button", { name: "Use the base prompt" }))
    expect(runCommand).toHaveBeenLastCalledWith("/prompt clear")
  })

  it("shows the Go marks of a model menu as icons and checks the current entry", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(
      reply("✅ Pick a provider.", [
        [
          { text: "✓ ⚠️ old-model", data: "/model old-model" },
          { text: "gpt-5", data: "/model gpt-5" },
        ],
        [{ text: "Next ▶", data: "/model page 2" }],
      ]),
    )
    const user = userEvent.setup()
    await mk()
    await user.click(modelChip())
    await flushPromises()
    const menu = screen.getByRole("menu")
    expect(menu.querySelector("svg.glyph")).not.toBeNull()
    expect(menu.textContent).toContain("Pick a provider.")
    expect(menu.textContent).not.toMatch(/[✅✓⚠▶]/u)
    const old = screen.getAllByRole("menuitem").find((i) => i.textContent!.includes("old-model"))!
    // A menuitem does not take aria-checked, so the current item says "current" to a screen reader.
    expect(old.hasAttribute("aria-checked")).toBe(false)
    expect(within(old).getByText("current").classList).toContain("sr-only")
    expect(old.querySelector("svg.lucide-check")).not.toBeNull()
    expect(old.querySelector("svg.lucide-triangle-alert")!.classList.contains("text-warning")).toBe(
      true,
    )
    expect(old.textContent).toContain("not usable")
    expect(screen.getByRole("menuitem", { name: "gpt-5" }).textContent).not.toContain("current")
    await user.click(screen.getByRole("menuitem", { name: "Next" }))
    expect(runCommand).toHaveBeenLastCalledWith("/model page 2")
  })

  it("shows the effort chip with a chevron", async () => {
    await mk()
    const chip = screen.getByRole("button", { name: /^effort / })
    expect(chip.textContent!.trim()).toBe("effort default")
    expect(chip.querySelector("svg.lucide-chevron-down")).not.toBeNull()
  })

  it("shows the buttons of a reply popover with the current one pressed", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(
      reply("🛡 **Mode:** Guarded", [
        [
          { text: "👁 Read-only", data: "/mode read-only" },
          { text: "🛡 Guarded ✓", data: "/mode guarded" },
          { text: "⚡ Full", data: "/mode full" },
        ],
      ]),
    )
    const user = userEvent.setup()
    await mk()
    await user.click(screen.getByRole("button", { name: "Old" }))
    await user.type(screen.getByRole("textbox", { name: "Session title" }), "x{Enter}")
    await flushPromises()
    const pop = await screen.findByRole("dialog")
    expect(pop.querySelector("svg.glyph")).not.toBeNull()
    expect(pop.textContent).not.toMatch(/[🛡👁⚡✓]/u)
    const guarded = within(pop).getByRole("button", { name: "Guarded" })
    expect(guarded.getAttribute("aria-pressed")).toBe("true")
    expect(guarded.querySelector("svg.lucide-check")).not.toBeNull()
    expect(
      within(pop).getByRole("button", { name: "Read-only" }).hasAttribute("aria-pressed"),
    ).toBe(false)
    await user.click(within(pop).getByRole("button", { name: "Full" }))
    expect(runCommand).toHaveBeenLastCalledWith("/mode full")
  })
})
